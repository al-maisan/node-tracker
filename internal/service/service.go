// Package service implements `node-tracker install-service`: it writes the
// shipped systemd units with ExecStart set to the running binary, and
// enables them.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/al-maisan/node-tracker/deploy"
	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/fsx"
)

// Account is the system user and group of the system units.
const Account = "node-tracker"

// Options are the install-service flags.
type Options struct {
	System bool   // system service (needs root); else a user service
	Timer  bool   // node-tracker-once.timer instead of the daemon
	DryRun bool   // print files and commands, change nothing
	Force  bool   // replace changed units, keeping a .bak
	Config string // user: config for the unit; system: config to install
}

// Commander runs a program directly, without a shell.
type Commander interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Exec is the real Commander.
type Exec struct{ Stdout, Stderr io.Writer }

func (e Exec) Run(ctx context.Context, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout, c.Stderr = e.Stdout, e.Stderr
	return c.Run()
}

// Host is the machine Install works on; tests fake it.
type Host struct {
	Cmd           Commander
	Out           io.Writer
	Euid          int
	Executable    func() (string, error)
	Owner         func() (uid, gid int, err error) // root and group Account; user.UnknownUserError if Account is missing
	AccountIDs    func() (uid int, gids []int, err error)
	LookupGroup   func(name string) (gid int, err error)
	EtcDir        string // system config and secrets
	SystemUnitDir string
	PackagedDirs  map[bool]string // packaged units, by Options.System
	ServiceEnv    config.Env      // where the system service detects the local bitcoind
}

// NewHost is the real machine.
func NewHost(stdout, stderr io.Writer) Host {
	return Host{
		Cmd:           Exec{stdout, stderr},
		Out:           stdout,
		Euid:          os.Geteuid(),
		Executable:    os.Executable,
		Owner:         owner,
		AccountIDs:    accountIDs,
		LookupGroup:   lookupGroup,
		EtcDir:        "/etc/node-tracker",
		SystemUnitDir: "/etc/systemd/system",
		PackagedDirs:  map[bool]string{true: "/usr/lib/systemd/system", false: "/usr/lib/systemd/user"},
		ServiceEnv:    config.ServiceEnv(),
	}
}

func owner() (int, int, error) {
	if _, err := user.Lookup(Account); err != nil {
		return 0, 0, err
	}
	g, err := user.LookupGroup(Account)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
	return 0, gid, err
}

// accountIDs are the uid and groups of Account.
func accountIDs() (int, []int, error) {
	u, err := user.Lookup(Account)
	if err != nil {
		return 0, nil, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, nil, err
	}
	ids, err := u.GroupIds()
	if err != nil {
		return 0, nil, err
	}
	var gids []int
	for _, id := range ids {
		if gid, err := strconv.Atoi(id); err == nil {
			gids = append(gids, gid)
		}
	}
	return uid, gids, nil
}

func lookupGroup(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}

// UsageError is a problem with the flags, the binary or the config.
type UsageError struct{ Err error }

func (e UsageError) Error() string { return e.Err.Error() }
func (e UsageError) Unwrap() error { return e.Err }

func usagef(format string, a ...any) error { return UsageError{fmt.Errorf(format, a...)} }

// Paths in a unit: nothing systemd would expand, split or unquote.
var pathRE = regexp.MustCompile(`^/[A-Za-z0-9._+/-]*$`)

// ProtectHome=yes in the system units hides these.
var protectedHome = []string{"/home", "/root", "/run/user"}

// Install writes and enables the units.
func Install(ctx context.Context, h Host, o Options) error {
	exe, err := h.Executable()
	if err != nil {
		return err
	}
	if exe, err = checkExecutable(exe, o.System); err != nil {
		return err
	}
	in := &installer{h: h, o: o}
	if o.System {
		return in.system(ctx, exe)
	}
	return in.user(ctx, exe)
}

type installer struct {
	h   Host
	o   Options
	own *owned // root:Account, for the system config and secrets
}

type owned struct{ uid, gid int }

// file is a file to write, or with data nil one whose owner and mode to set.
type file struct {
	path  string
	data  []byte
	mode  fs.FileMode
	owned bool // root:Account, else the caller's
	same  bool // set by plan: already has data
	bak   bool // set by plan: back up the old file
}

func (in *installer) user(ctx context.Context, exe string) error {
	path, args := in.o.Config, []string(nil)
	if path != "" {
		if err := checkPath(path); err != nil {
			return UsageError{fmt.Errorf("-config: %w", err)}
		}
		args = []string{"-config", path}
	} else {
		var err error
		if path, err = config.DefaultConfigPath(); err != nil {
			return err
		}
	}
	if _, err := in.loadConfig(path); err != nil {
		return err
	}
	dir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "systemd", "user")
	files, enable, err := units("user", dir, in.o.Timer, exe, args)
	if err != nil {
		return err
	}
	if files, err = in.packaged(files); err != nil {
		return err
	}
	if err := plan(files, in.o.Force); err != nil {
		return err
	}
	if !in.o.DryRun && len(files) > 0 {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := in.apply(files); err != nil {
		return err
	}
	if err := in.run(ctx, "loginctl", "enable-linger"); err != nil {
		fmt.Fprintf(in.h.Out, "could not enable linger (%v), so the service stops when you log out. It may need privileges: sudo loginctl enable-linger \"$USER\"\n", err)
	}
	return in.start(ctx, []string{"systemctl", "--user"}, enable)
}

func (in *installer) system(ctx context.Context, exe string) error {
	if in.h.Euid != 0 && !in.o.DryRun {
		return usagef("-system needs root")
	}
	dst := filepath.Join(in.h.EtcDir, "config.yaml")
	src := in.o.Config
	if src == dst {
		src = ""
	}
	load := dst
	if src != "" {
		load = src
	}
	cfg, err := in.loadConfig(load)
	if err != nil {
		if src == "" && errors.Is(err, fs.ErrNotExist) {
			return usagef("no config at %s; pass the config to install with -config", dst)
		}
		return err
	}
	cf := file{path: dst, mode: 0o640, owned: true}
	if src != "" {
		if cf.data, err = os.ReadFile(src); err != nil {
			return err
		}
	}
	secrets, err := secretFiles(cfg, in.h.EtcDir)
	if err != nil {
		return err
	}
	if err := in.checkCookies(cfg); err != nil {
		return err
	}
	files := []file{cf}
	for _, s := range secrets {
		files = append(files, file{path: s, mode: 0o640, owned: true})
	}
	us, enable, err := units("system", in.h.SystemUnitDir, in.o.Timer, exe, nil)
	if err != nil {
		return err
	}
	if us, err = in.packaged(us); err != nil {
		return err
	}
	files = append(files, us...)
	if err := plan(files, in.o.Force); err != nil {
		return err
	}
	if err := in.account(ctx); err != nil {
		return err
	}
	if !in.o.DryRun {
		if err := os.Mkdir(in.h.EtcDir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if err := in.apply(append([]file{{path: in.h.EtcDir, mode: fs.ModeDir | 0o750, owned: true}}, files...)); err != nil {
		return err
	}
	return in.start(ctx, []string{"systemctl"}, enable)
}

// account looks up Account and creates it if it is missing.
func (in *installer) account(ctx context.Context) error {
	uid, gid, err := in.h.Owner()
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		if err := in.run(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", Account); err != nil {
			return fmt.Errorf("creating user %s: %w", Account, err)
		}
		if in.o.DryRun {
			in.own = &owned{-1, -1}
			return nil
		}
		uid, gid, err = in.h.Owner()
	}
	if err != nil {
		return fmt.Errorf("user %s: %w", Account, err)
	}
	in.own = &owned{uid, gid}
	return nil
}

// loadConfig validates the config the service will use, detecting the
// local bitcoind as the service will: the system service has no home.
func (in *installer) loadConfig(path string) (*config.Config, error) {
	env := config.SystemEnv()
	if in.o.System {
		env = in.h.ServiceEnv
	}
	cfg, warns, err := config.LoadEnv(path, env)
	for _, w := range warns {
		fmt.Fprintf(in.h.Out, "config warning: %s\n", w)
	}
	if err != nil {
		return nil, UsageError{err}
	}
	// Not an error: a drop-in may set the variable.
	edit := "systemctl --user edit node-tracker"
	if in.o.System {
		edit = "systemctl edit node-tracker"
	}
	for i, n := range cfg.Notifiers {
		if n.BotTokenEnv != "" {
			fmt.Fprintf(in.h.Out, "config warning: notifiers[%d].bot_token_env: the service does not get this shell's environment; use bot_token_file, or set the variable in a drop-in (%s: Environment= or EnvironmentFile=), or the service won't start\n", i, edit)
		}
	}
	for _, e := range endpoints(cfg) {
		if e.PasswordEnv != "" {
			fmt.Fprintf(in.h.Out, "config warning: %s: password_env: the service does not get this shell's environment; use password_file, or set the variable in a drop-in (%s: Environment= or EnvironmentFile=), or the service won't start\n", e.Name, edit)
		}
	}
	return cfg, nil
}

func endpoints(cfg *config.Config) []config.Endpoint {
	var es []config.Endpoint
	for _, t := range cfg.Targets {
		es = append(append(es, t.Node), t.References...)
	}
	return es
}

// secretFiles are the config's secret files. The system service reads them
// from dir only, where install-service sets their owner and mode.
func secretFiles(cfg *config.Config, dir string) ([]string, error) {
	var paths []string
	for _, n := range cfg.Notifiers {
		paths = append(paths, n.BotTokenFile)
	}
	for _, e := range endpoints(cfg) {
		paths = append(paths, e.PasswordFile)
	}
	var out []string
	for _, p := range paths {
		if p == "" || slices.Contains(out, p) {
			continue
		}
		if filepath.Dir(p) != dir || filepath.Base(p) == "config.yaml" {
			return nil, usagef("secret file %s: put it in %s and point the config at it", p, dir)
		}
		out = append(out, p)
	}
	return out, nil
}

// checkCookies makes sure the system service can read every cookie_file:
// ProtectHome=yes hides /home, /root and /run/user, and elsewhere Account
// needs read access through its user, its groups or SupplementaryGroups= in
// a drop-in of the service. ACLs are not considered.
func (in *installer) checkCookies(cfg *config.Config) error {
	uid, gids, err := in.h.AccountIDs()
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		uid, gids, err = -1, nil, nil // created later, with a new group
	}
	if err != nil {
		return fmt.Errorf("user %s: %w", Account, err)
	}
	svc := serviceName(in.o.Timer)
	groups, err := dropInGroups(filepath.Join(in.h.SystemUnitDir, svc+".d"))
	if err != nil {
		return err
	}
	for _, g := range groups {
		if gid, err := in.h.LookupGroup(g); err == nil {
			gids = append(gids, gid)
		}
	}
	for _, e := range endpoints(cfg) {
		if e.CookieFile == "" {
			continue
		}
		// Both the path as written and where its symlinks lead must be
		// outside ProtectHome's dirs.
		hidden := func(p string) error {
			if d, ok := inProtectedHome(p); ok {
				return usagef("%s: cookie_file %s: the system service can't read it under %s (ProtectHome=yes); point bitcoind's rpccookiefile outside it, or use username and password_file", e.Name, e.CookieFile, d)
			}
			return nil
		}
		if err := hidden(e.CookieFile); err != nil {
			return err
		}
		if _, err := os.Stat(e.CookieFile); errors.Is(err, fs.ErrNotExist) {
			return usagef("%s: cookie_file %s does not exist; start bitcoind first, so its access can be checked", e.Name, e.CookieFile)
		}
		real, err := filepath.EvalSymlinks(e.CookieFile)
		if err != nil {
			return err
		}
		if err := hidden(real); err != nil {
			return err
		}
		bs, gid, err := blockers(e.CookieFile, uid, gids)
		if err != nil {
			return err
		}
		if len(bs) == 0 {
			continue
		}
		return cookieError(e.CookieFile, uid, gids, gid)
	}
	return nil
}

// dropInGroups are the SupplementaryGroups= of the *.conf drop-ins in dir.
func dropInGroups(dir string) ([]string, error) {
	confs, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return nil, err
	}
	slices.Sort(confs) // systemd's order, so an empty assignment resets the earlier ones
	var groups []string
	for _, c := range confs {
		b, err := os.ReadFile(c)
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "SupplementaryGroups="); ok {
				if v = strings.TrimSpace(v); v == "" {
					groups = nil
				}
				groups = append(groups, strings.Fields(v)...)
			}
		}
	}
	return groups, nil
}

// blocker is a part of a path that keeps a user out: a directory without
// x or the file without r for them.
type blocker struct {
	path     string
	uid, gid int
	perm     fs.FileMode
	dir      bool
}

// blockers lists every part of path, where its symlinks lead, that uid with
// gids can't get through, and returns the file's group.
func blockers(path string, uid int, gids []int) ([]blocker, int, error) {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, 0, err
	}
	var bs []blocker
	gid := -1
	for q, need := p, fs.FileMode(4); ; q, need = filepath.Dir(q), 1 {
		fi, err := os.Stat(q)
		if err != nil {
			return nil, 0, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, 0, fmt.Errorf("%s: no owner information", q)
		}
		if gid == -1 {
			gid = int(st.Gid)
		}
		perm := fi.Mode().Perm()
		bits := perm & 7
		switch {
		case int(st.Uid) == uid:
			bits = perm >> 6 & 7
		case slices.Contains(gids, int(st.Gid)):
			bits = perm >> 3 & 7
		}
		if bits&need == 0 {
			bs = append(bs, blocker{q, int(st.Uid), int(st.Gid), perm, q != p})
		}
		if q == "/" {
			return bs, gid, nil
		}
	}
}

// cookieError tells in plain words why the service account (uid, gids)
// can't read the cookie at path, whose group is gid, and how to fix it as
// commands: join that group, then open each path that still blocks. bitcoind
// rewrites the cookie on start, so its mode comes from rpccookieperms.
func cookieError(path string, uid int, gids []int, gid int) error {
	g, in := groupName(gid), slices.Contains(gids, gid)
	joined := append(slices.Clone(gids), gid)
	bs, _, err := blockers(path, uid, joined)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the service can't read bitcoind's cookie %s.\n", path)
	switch {
	case in:
		fmt.Fprintf(&b, "It runs as user %s, which is in the cookie's group %s, but:\n", Account, g)
	case len(bs) > 0:
		fmt.Fprintf(&b, "It runs as user %s, which is not in the cookie's group %s, and:\n", Account, g)
	default:
		fmt.Fprintf(&b, "It runs as user %s, which is not in the cookie's group %s.\n", Account, g)
	}
	var fixes []string
	if !in {
		fixes = append(fixes, "sudo usermod -aG "+g+" "+Account)
	}
	w := 0
	for _, x := range bs {
		w = max(w, len(x.path))
	}
	for _, x := range bs {
		why, fix := "its group can't read it", "add rpccookieperms=group to bitcoin.conf (Bitcoin Core 28 or newer), then restart bitcoind"
		if x.dir {
			why, fix = dirFix(x, uid, joined)
		}
		fmt.Fprintf(&b, "  %-*s  owner %s, group %s, mode %04o: %s\n", w, x.path, userName(x.uid), groupName(x.gid), x.perm, why)
		fixes = append(fixes, fix)
	}
	b.WriteString("To fix it:")
	for i, f := range fixes {
		fmt.Fprintf(&b, "\n  %d. %s", i+1, f)
	}
	return usagef("%s", b.String())
}

// dirFix is why the directory x blocks uid with gids, and the chmod that
// opens it: by the same bits as blockers, the owner's, the group's or the
// others'.
func dirFix(x blocker, uid int, gids []int) (why, fix string) {
	switch {
	case x.uid == uid:
		return "its owner can't open it", "sudo chmod u+x " + x.path
	case slices.Contains(gids, x.gid):
		return "its group can't open it", "sudo chmod g+x " + x.path
	}
	return "others can't open it", "sudo chmod o+x " + x.path
}

// userName is uid's name, else the number.
func userName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

// groupName is gid's name, else the number.
func groupName(gid int) string {
	if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
		return g.Name
	}
	return strconv.Itoa(gid)
}

// packaged returns the unit files to write. If the package's units (deb,
// rpm) exist and run this binary with the same ExecStart=, there are none:
// a unit in /etc or ~/.config would shadow them, and package updates of the
// units would be ignored. If they exist but differ, writing needs -force.
func (in *installer) packaged(files []file) ([]file, error) {
	dir := in.h.PackagedDirs[in.o.System]
	if dir == "" {
		return files, nil
	}
	found, same := false, true
	for _, f := range files {
		b, err := fsx.ReadRegular(filepath.Join(dir, filepath.Base(f.path)))
		if errors.Is(err, fs.ErrNotExist) {
			same = false
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		same = same && execStart(b) == execStart(f.data)
	}
	switch {
	case !found:
		return files, nil
	case same:
		for _, f := range files {
			fmt.Fprintf(in.h.Out, "using the packaged %s\n", filepath.Join(dir, filepath.Base(f.path)))
			if _, err := os.Lstat(f.path); err == nil {
				fmt.Fprintf(in.h.Out, "warning: %s shadows the packaged unit; remove it, then run install-service again\n", f.path)
			}
		}
		return nil, nil
	case !in.o.Force:
		return nil, fmt.Errorf("the packaged units in %s run another binary or other flags than this install would; a unit in %s would shadow them and hide package updates of the units. Run the packaged binary's install-service, or rerun with -force to write the units anyway", dir, filepath.Dir(files[0].path))
	}
	fmt.Fprintf(in.h.Out, "warning: the units in %s shadow the packaged ones in %s\n", filepath.Dir(files[0].path), dir)
	return files, nil
}

// execStart is the ExecStart= line of a unit, "" if it has none.
func execStart(unit []byte) string {
	for _, l := range strings.Split(string(unit), "\n") {
		if strings.HasPrefix(l, "ExecStart=") {
			return l
		}
	}
	return ""
}

func serviceName(timer bool) string {
	if timer {
		return "node-tracker-once.service"
	}
	return "node-tracker.service"
}

// units renders the units of one kind (system or user) and names the one to
// enable.
func units(kind, dir string, timer bool, exe string, args []string) ([]file, string, error) {
	names := []string{serviceName(timer)}
	if timer {
		names = append(names, "node-tracker-once.timer")
	}
	var files []file
	for _, n := range names {
		data, err := deploy.Systemd.ReadFile("systemd/" + kind + "/" + n)
		if err != nil {
			return nil, "", err
		}
		if strings.HasSuffix(n, ".service") {
			if data, err = render(data, exe, args); err != nil {
				return nil, "", fmt.Errorf("%s/%s: %w", kind, n, err)
			}
		}
		files = append(files, file{path: filepath.Join(dir, n), data: data, mode: 0o644})
	}
	return files, names[len(names)-1], nil
}

// render sets the binary of the template's ExecStart= to exe and appends
// args to it. The rest of the unit, its hardening included, stays as
// shipped. exe and args must be validated: they are not quoted.
func render(tmpl []byte, exe string, args []string) ([]byte, error) {
	var out []string
	n := 0
	for _, l := range strings.SplitAfter(string(tmpl), "\n") {
		rest, ok := strings.CutPrefix(l, "ExecStart=")
		if !ok {
			out = append(out, l)
			continue
		}
		n++
		var bin bool
		for _, p := range []string{"/usr/local/bin/node-tracker ", "%h/.local/bin/node-tracker "} {
			if rest, ok = strings.CutPrefix(rest, p); ok {
				bin = true
				break
			}
		}
		if !bin {
			return nil, errors.New("ExecStart= does not start with the shipped node-tracker path")
		}
		line := "ExecStart=" + strings.Join(slices.Concat([]string{exe, strings.TrimSuffix(rest, "\n")}, args), " ") + "\n"
		out = append(out, line)
	}
	if n != 1 {
		return nil, fmt.Errorf("want one ExecStart=, found %d", n)
	}
	return []byte(strings.Join(out, "")), nil
}

// checkExecutable resolves the running binary and checks that a unit can
// run it safely.
func checkExecutable(exe string, system bool) (string, error) {
	p, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	if err := checkPath(p); err != nil {
		return "", UsageError{fmt.Errorf("binary: %w", err)}
	}
	if d, ok := inProtectedHome(p); system && ok {
		return "", usagef("binary %s: the system unit can't run it from %s (ProtectHome=yes); install it to e.g. /usr/local/bin and run that", p, d)
	}
	if fi, err := os.Stat(p); err != nil {
		return "", err
	} else if !fi.Mode().IsRegular() {
		return "", usagef("binary %s is not a regular file", p)
	}
	// The system service can read the secrets, so only root may change the
	// code it runs: the binary and every directory above it.
	for q := p; ; q = filepath.Dir(q) {
		fi, err := os.Stat(q)
		if err != nil {
			return "", err
		}
		if system && !rootOnly(fi) {
			return "", usagef("binary %s: %s must be owned by root and not group- or world-writable; install the binary to e.g. /usr/local/bin and run that", p, q)
		}
		if !system && fi.Mode().Perm()&0o002 != 0 && (q == p || q == filepath.Dir(p)) {
			return "", usagef("binary: %s is world-writable", q)
		}
		if q == "/" {
			return p, nil
		}
	}
}

// rootOnly: owned by root, and only the owner may write.
func rootOnly(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && fi.Mode().Perm()&0o022 == 0
}

// inProtectedHome returns the dir of protectedHome that holds p.
func inProtectedHome(p string) (string, bool) {
	for _, d := range protectedHome {
		if p == d || strings.HasPrefix(p, d+"/") {
			return d, true
		}
	}
	return "", false
}

func checkPath(p string) error {
	if !pathRE.MatchString(p) || filepath.Clean(p) != p {
		return fmt.Errorf("%q must be a clean absolute path of letters, digits and ._+-", p)
	}
	return nil
}

// plan checks every file before anything changes: a file that differs from
// what would be written is replaced only with force, and backed up.
func plan(files []file, force bool) error {
	var errs []error
	for i := range files {
		f := &files[i]
		if f.data == nil {
			continue
		}
		old, err := fsx.ReadRegular(f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			errs = append(errs, err)
		case bytes.Equal(old, f.data):
			f.same = true
		case !force:
			errs = append(errs, fmt.Errorf("%s has changed; check it, then rerun with -force to replace it (keeps %[1]s.bak)", f.path))
		default:
			f.bak = true
		}
	}
	return errors.Join(errs...)
}

func (in *installer) apply(files []file) error {
	for _, f := range files {
		if err := in.applyOne(f); err != nil {
			return err
		}
	}
	return nil
}

func (in *installer) applyOne(f file) error {
	who := ""
	if f.owned {
		who = " root:" + Account
	}
	mode := fmt.Sprintf("%04o", f.mode.Perm())
	switch {
	case in.o.DryRun && (f.data == nil || f.same):
		if f.owned {
			fmt.Fprintf(in.h.Out, "would set %s to %s%s\n", f.path, mode, who)
		} else {
			fmt.Fprintf(in.h.Out, "unchanged %s\n", f.path)
		}
		return nil
	case in.o.DryRun:
		if f.bak {
			fmt.Fprintf(in.h.Out, "would back up %s to %[1]s.bak\n", f.path)
		}
		fmt.Fprintf(in.h.Out, "would write %s (%s%s):\n%s", f.path, mode, who, f.data)
		return nil
	case f.data == nil || f.same:
		if !f.owned {
			fmt.Fprintf(in.h.Out, "unchanged %s\n", f.path)
			return nil
		}
		fd, err := os.OpenFile(f.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer fd.Close()
		fi, err := fd.Stat()
		if err != nil {
			return err
		}
		if fi.Mode().Type() != f.mode.Type() {
			return fmt.Errorf("%s: unexpected file type %v", f.path, fi.Mode().Type())
		}
		if err := fd.Chown(in.own.uid, in.own.gid); err != nil {
			return err
		}
		if err := fd.Chmod(f.mode.Perm()); err != nil {
			return err
		}
		fmt.Fprintf(in.h.Out, "set %s to %s%s\n", f.path, mode, who)
		return nil
	}
	if err := in.write(f); err != nil {
		return fmt.Errorf("writing %s: %w", f.path, err)
	}
	if f.bak {
		fmt.Fprintf(in.h.Out, "backed up %s to %[1]s.bak\n", f.path)
	}
	fmt.Fprintf(in.h.Out, "wrote %s (%s%s)\n", f.path, mode, who)
	return nil
}

// write replaces f.path atomically, keeping the old file as .bak if f.bak.
func (in *installer) write(f file) error {
	uid, gid := -1, -1
	if f.owned {
		uid, gid = in.own.uid, in.own.gid
	}
	return fsx.WriteFile(f.path, f.data, f.mode, uid, gid, f.bak)
}

// start reloads systemd, enables and starts unit, and shows its status.
func (in *installer) start(ctx context.Context, systemctl []string, unit string) error {
	if err := in.run(ctx, slices.Concat(systemctl, []string{"daemon-reload"})...); err != nil {
		return err
	}
	err := in.run(ctx, slices.Concat(systemctl, []string{"enable", "--now", unit})...)
	// status exits non-zero for a unit that is not running; it is shown for
	// information only.
	in.run(ctx, slices.Concat(systemctl, []string{"status", "--no-pager", unit})...)
	return err
}

func (in *installer) run(ctx context.Context, args ...string) error {
	if in.o.DryRun {
		fmt.Fprintf(in.h.Out, "would run: %s\n", strings.Join(args, " "))
		return nil
	}
	fmt.Fprintf(in.h.Out, "run: %s\n", strings.Join(args, " "))
	if err := in.h.Cmd.Run(ctx, args[0], args[1:]...); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
	}
	return nil
}
