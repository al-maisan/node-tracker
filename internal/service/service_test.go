package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/deploy"
	"github.com/al-maisan/node-tracker/internal/config"
)

// fakeCmd records the commands instead of running them.
type fakeCmd struct {
	calls []string
	fail  map[string]error // by program name
	ran   func(name string)
}

func (f *fakeCmd) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if f.ran != nil {
		f.ran(name)
	}
	return f.fail[name]
}

type env struct {
	h      Host
	cmd    *fakeCmd
	out    *bytes.Buffer
	exe    string // resolved binary
	conf   string // user config dir ($XDG_CONFIG_HOME)
	cookie string
}

// newEnv fakes a host whose node-tracker account is the test user, with a
// user config whose token sits in the system config dir and whose cookie
// is readable by the test user's group.
func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	e := &env{cmd: &fakeCmd{}, out: &bytes.Buffer{}, exe: filepath.Join(dir, "bin", "node-tracker"), conf: filepath.Join(dir, "xdg"), cookie: filepath.Join(dir, "bitcoin", ".cookie")}
	etc := filepath.Join(dir, "etc")
	for _, d := range []string{filepath.Dir(dir), dir, filepath.Dir(e.exe), filepath.Join(e.conf, "node-tracker"), etc, filepath.Dir(e.cookie)} {
		mkdir(t, d, 0o750)
	}
	write(t, e.exe, "binary", 0o755)
	write(t, e.cookie, "__cookie__:x", 0o640)
	write(t, filepath.Join(etc, "token"), "TOKEN", 0o600)
	write(t, filepath.Join(e.conf, "node-tracker", "config.yaml"), testConfig(filepath.Join(etc, "token"), e.cookie), 0o600)
	t.Setenv("XDG_CONFIG_HOME", e.conf)
	e.h = Host{
		Cmd:        e.cmd,
		Out:        e.out,
		Euid:       os.Geteuid(),
		Executable: func() (string, error) { return e.exe, nil },
		Owner:      func() (int, int, error) { return os.Getuid(), os.Getgid(), nil },
		AccountIDs: func() (int, []int, error) { return os.Getuid(), nil, nil },
		LookupGroup: func(name string) (int, error) {
			if name == "bitcoin" {
				return os.Getgid(), nil
			}
			return 0, user.UnknownGroupError(name)
		},
		EtcDir:        etc,
		SystemUnitDir: filepath.Join(dir, "units"),
		PackagedDirs:  map[bool]string{true: filepath.Join(dir, "pkg", "system"), false: filepath.Join(dir, "pkg", "user")},
	}
	mkdir(t, e.h.SystemUnitDir, 0o755)
	return e
}

// rootExe makes the binary one that only root can change, as -system needs.
func rootExe(t *testing.T, e *env) {
	p, err := checkExecutable("/usr/bin/true", true)
	if err != nil {
		t.Skipf("no root-owned binary for -system: %v", err)
	}
	e.exe = p
}

// dropIn writes a drop-in for the system unit svc.
func dropIn(t *testing.T, e *env, svc, data string) {
	d := filepath.Join(e.h.SystemUnitDir, svc+".d")
	mkdir(t, d, 0o755)
	write(t, filepath.Join(d, "groups.conf"), data, 0o644)
}

func testConfig(token, cookie string) string {
	return fmt.Sprintf(`targets:
  - name: btc
    node: {kind: bitcoind, url: "http://127.0.0.1:8332", cookie_file: %s}
    references: [{kind: esplora, url: "https://mempool.space/api"}]
    alert: {threshold: 3}
    notify: [tg]
notifiers:
  - {name: tg, kind: telegram, bot_token_file: %s, chat_id: "42"}
`, cookie, token)
}

func mkdir(t *testing.T, d string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(d, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, mode); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func shipped(t *testing.T, name string) string {
	t.Helper()
	b, err := deploy.Systemd.ReadFile("systemd/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// withoutExec drops the line render changes.
func withoutExec(s string) []string {
	return slices.DeleteFunc(strings.Split(s, "\n"), func(l string) bool { return strings.HasPrefix(l, "ExecStart=") })
}

func TestRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		exec string
	}{
		{"system/node-tracker.service", nil, "ExecStart=/opt/nt/node-tracker run -config /etc/node-tracker/config.yaml -state /var/lib/node-tracker/state.json"},
		{"system/node-tracker-once.service", nil, "ExecStart=/opt/nt/node-tracker once -config /etc/node-tracker/config.yaml -state /var/lib/node-tracker/state.json"},
		{"user/node-tracker.service", nil, "ExecStart=/opt/nt/node-tracker run"},
		{"user/node-tracker-once.service", []string{"-config", "/c.yaml"}, "ExecStart=/opt/nt/node-tracker once -config /c.yaml"},
	} {
		tmpl := shipped(t, tc.name)
		b, err := render([]byte(tmpl), "/opt/nt/node-tracker", tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := string(b)
		if !strings.Contains(got, "\n"+tc.exec+"\n") {
			t.Errorf("%s: no %q in\n%s", tc.name, tc.exec, got)
		}
		if !slices.Equal(withoutExec(got), withoutExec(tmpl)) {
			t.Errorf("%s: changed more than ExecStart=:\n%s", tc.name, got)
		}
	}
	for _, tmpl := range []string{
		"[Service]\n",
		"ExecStart=/usr/local/bin/node-tracker run\nExecStart=/usr/local/bin/node-tracker once\n",
		"ExecStart=/usr/bin/node-tracker run\n",
	} {
		if _, err := render([]byte(tmpl), "/x", nil); err == nil {
			t.Errorf("%q: no error", tmpl)
		}
	}
}

func TestCheckExecutable(t *testing.T) {
	e := newEnv(t)
	link := filepath.Join(t.TempDir(), "nt")
	if err := os.Symlink(e.exe, link); err != nil {
		t.Fatal(err)
	}
	if p, err := checkExecutable(link, false); err != nil || p != e.exe {
		t.Fatalf("symlink: %q, %v; want %q", p, err, e.exe)
	}

	ww := filepath.Join(t.TempDir(), "ww")
	mkdir(t, ww, 0o777)
	write(t, filepath.Join(ww, "nt"), "x", 0o755)
	spaced := filepath.Join(t.TempDir(), "a b")
	mkdir(t, spaced, 0o755)
	write(t, filepath.Join(spaced, "nt"), "x", 0o755)
	write(t, filepath.Join(filepath.Dir(e.exe), "ww"), "x", 0o757)
	for _, p := range []string{filepath.Join(ww, "nt"), filepath.Join(spaced, "nt"), filepath.Join(filepath.Dir(e.exe), "ww"), filepath.Dir(e.exe)} {
		if _, err := checkExecutable(p, false); !errors.As(err, new(UsageError)) {
			t.Errorf("%s: err = %v, want a UsageError", p, err)
		}
	}

	// -system: only root may be able to change the binary or a directory above it.
	if _, err := checkExecutable(e.exe, true); !errors.As(err, new(UsageError)) {
		t.Errorf("-system, binary in a temp dir: err = %v", err)
	}
	if p, err := checkExecutable("/usr/bin/true", true); err != nil {
		t.Logf("skipping the root-owned case: %v", err)
	} else if p == "" {
		t.Error("no path")
	}
	root := fakeInfo{0, 0o755}
	for fi, want := range map[fakeInfo]bool{root: true, {1000, 0o755}: false, {0, 0o775}: false, {0, 0o757}: false} {
		if got := rootOnly(fi); got != want {
			t.Errorf("rootOnly(%+v) = %v", fi, got)
		}
	}

	for p, want := range map[string]bool{"/home/u/bin/nt": true, "/root/nt": true, "/run/user/1000/nt": true, "/homes/nt": false, "/usr/local/bin/nt": false} {
		if _, got := inProtectedHome(p); got != want {
			t.Errorf("inProtectedHome(%s) = %v", p, got)
		}
	}
}

// fakeInfo is a file with an owner and mode, for rootOnly.
type fakeInfo struct {
	uid  uint32
	perm os.FileMode
}

func (f fakeInfo) Name() string       { return "f" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return f.perm }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

func TestInstallUser(t *testing.T) {
	e := newEnv(t)
	unit := filepath.Join(e.conf, "systemd", "user", "node-tracker.service")
	if err := Install(context.Background(), e.h, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, unit); !strings.Contains(got, "\nExecStart="+e.exe+" run\n") || mode(t, unit) != 0o644 {
		t.Fatalf("unit (%v):\n%s", mode(t, unit), got)
	}
	want := []string{
		"loginctl enable-linger",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now node-tracker.service",
		"systemctl --user status --no-pager node-tracker.service",
	}
	if !slices.Equal(e.cmd.calls, want) {
		t.Fatalf("calls = %q", e.cmd.calls)
	}

	// Same content again: nothing to protect.
	if err := Install(context.Background(), e.h, Options{}); err != nil || !strings.Contains(e.out.String(), "unchanged "+unit) {
		t.Fatalf("rerun: %v\n%s", err, e.out)
	}

	// A changed unit is kept unless forced, and then backed up.
	rendered := read(t, unit)
	write(t, unit, rendered+"# local edit\n", 0o644)
	e.cmd.calls = nil
	if err := Install(context.Background(), e.h, Options{}); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("changed unit: err = %v", err)
	}
	if read(t, unit) != rendered+"# local edit\n" || len(e.cmd.calls) != 0 {
		t.Fatalf("changed unit was touched or commands ran: %q", e.cmd.calls)
	}
	write(t, unit+".bak", "stale", 0o644)
	old, err := os.Stat(unit)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), e.h, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if read(t, unit) != rendered || read(t, unit+".bak") != rendered+"# local edit\n" {
		t.Fatal("-force: wrong unit or backup")
	}
	// The backup is the old file itself (a link), so the unit never went missing.
	if bak, err := os.Stat(unit + ".bak"); err != nil || !os.SameFile(old, bak) {
		t.Fatalf("backup is not a link to the old unit: %v", err)
	}
}

func TestInstallUserPackaged(t *testing.T) {
	e := newEnv(t)
	pkg := e.h.PackagedDirs[false]
	mkdir(t, pkg, 0o755)
	unit := filepath.Join(e.conf, "systemd", "user", "node-tracker.service")

	// The package's unit runs another binary: writing ours would shadow it.
	other := strings.Replace(shipped(t, "user/node-tracker.service"), "%h/.local/bin/node-tracker", "/usr/bin/node-tracker", 1)
	write(t, filepath.Join(pkg, "node-tracker.service"), other, 0o644)
	if err := Install(context.Background(), e.h, Options{}); err == nil || !strings.Contains(err.Error(), "shadow") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(unit); err == nil || len(e.cmd.calls) != 0 {
		t.Fatalf("wrote the unit or ran %q", e.cmd.calls)
	}
	if err := Install(context.Background(), e.h, Options{Force: true}); err != nil || !strings.Contains(e.out.String(), "warning: the units in") {
		t.Fatalf("-force: %v\n%s", err, e.out)
	}
	if err := os.Remove(unit); err != nil {
		t.Fatal(err)
	}

	// It runs this binary: enable it, write nothing.
	b, err := render([]byte(shipped(t, "user/node-tracker.service")), e.exe, nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(pkg, "node-tracker.service"), string(b), 0o644)
	e.cmd.calls = nil
	if err := Install(context.Background(), e.h, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); err == nil || !slices.Contains(e.cmd.calls, "systemctl --user enable --now node-tracker.service") {
		t.Fatalf("wrote the unit, or calls = %q", e.cmd.calls)
	}
	if !strings.Contains(e.out.String(), "using the packaged "+filepath.Join(pkg, "node-tracker.service")) {
		t.Errorf("out:\n%s", e.out)
	}
	// -timer: the package has no once units here, so they are written.
	if err := Install(context.Background(), e.h, Options{Timer: true}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallUserTimerConfig(t *testing.T) {
	e := newEnv(t)
	cfg := filepath.Join(e.conf, "node-tracker", "config.yaml")
	if err := Install(context.Background(), e.h, Options{Timer: true, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.conf, "systemd", "user")
	svc := read(t, filepath.Join(dir, "node-tracker-once.service"))
	if !strings.Contains(svc, "\nExecStart="+e.exe+" once -config "+cfg+"\n") {
		t.Errorf("once.service:\n%s", svc)
	}
	if read(t, filepath.Join(dir, "node-tracker-once.timer")) != shipped(t, "user/node-tracker-once.timer") {
		t.Error("timer differs from the shipped one")
	}
	if _, err := os.Stat(filepath.Join(dir, "node-tracker.service")); err == nil {
		t.Error("-timer wrote the daemon unit")
	}
	if !slices.Contains(e.cmd.calls, "systemctl --user enable --now node-tracker-once.timer") {
		t.Errorf("calls = %q", e.cmd.calls)
	}

	if err := Install(context.Background(), e.h, Options{Config: filepath.Join(e.conf, "my config.yaml")}); !errors.As(err, new(UsageError)) {
		t.Errorf("config path with a space: err = %v", err)
	}
}

func TestInstallUserDryRun(t *testing.T) {
	e := newEnv(t)
	if err := Install(context.Background(), e.h, Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.conf, "systemd")); err == nil || len(e.cmd.calls) != 0 {
		t.Fatalf("dry run changed something: %q", e.cmd.calls)
	}
	out := e.out.String()
	for _, s := range []string{
		"would write " + filepath.Join(e.conf, "systemd", "user", "node-tracker.service") + " (0644):\n",
		"\nExecStart=" + e.exe + " run\n",
		"would run: loginctl enable-linger\n",
		"would run: systemctl --user enable --now node-tracker.service\n",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in\n%s", s, out)
		}
	}
}

func TestInstallUserCommandFailures(t *testing.T) {
	e := newEnv(t)
	e.cmd.fail = map[string]error{"loginctl": errors.New("access denied")}
	if err := Install(context.Background(), e.h, Options{}); err != nil {
		t.Fatalf("linger failure must not fail the install: %v", err)
	}
	if !strings.Contains(e.out.String(), "sudo loginctl enable-linger") || len(e.cmd.calls) != 4 {
		t.Fatalf("calls = %q\n%s", e.cmd.calls, e.out)
	}

	e.cmd.calls, e.cmd.fail = nil, map[string]error{"systemctl": errors.New("exit status 1")}
	if err := Install(context.Background(), e.h, Options{}); err == nil || !strings.Contains(err.Error(), "daemon-reload") {
		t.Fatalf("err = %v", err)
	}
	if len(e.cmd.calls) != 2 {
		t.Fatalf("ran on after a failed daemon-reload: %q", e.cmd.calls)
	}
}

func TestInstallUserBadConfig(t *testing.T) {
	e := newEnv(t)
	write(t, filepath.Join(e.conf, "node-tracker", "config.yaml"), "targets: []\n", 0o600)
	if err := Install(context.Background(), e.h, Options{}); !errors.As(err, new(UsageError)) {
		t.Fatalf("err = %v", err)
	}
	if len(e.cmd.calls) != 0 {
		t.Fatalf("calls = %q", e.cmd.calls)
	}
}

func TestInstallSystem(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	e.h.Euid = 0
	// The account is created by useradd, so only the drop-in's group gives
	// it the cookie.
	e.h.AccountIDs = func() (int, []int, error) { return 0, nil, user.UnknownUserError(Account) }
	dropIn(t, e, "node-tracker.service", "[Service]\nSupplementaryGroups=bitcoin\n")
	exists := false
	e.h.Owner = func() (int, int, error) {
		if !exists {
			return 0, 0, user.UnknownUserError(Account)
		}
		return os.Getuid(), os.Getgid(), nil
	}
	e.cmd.ran = func(name string) { exists = exists || name == "useradd" }
	src := filepath.Join(e.conf, "node-tracker", "config.yaml")
	if err := Install(context.Background(), e.h, Options{System: true, Config: src}); err != nil {
		t.Fatalf("%v\n%s", err, e.out)
	}
	cfg := filepath.Join(e.h.EtcDir, "config.yaml")
	unit := filepath.Join(e.h.SystemUnitDir, "node-tracker.service")
	for p, m := range map[string]os.FileMode{e.h.EtcDir: 0o750, cfg: 0o640, filepath.Join(e.h.EtcDir, "token"): 0o640, unit: 0o644} {
		if got := mode(t, p); got != m {
			t.Errorf("%s: mode %v, want %v", p, got, m)
		}
	}
	if read(t, cfg) != read(t, src) {
		t.Error("config not copied")
	}
	got := read(t, unit)
	if !strings.Contains(got, "\nExecStart="+e.exe+" run -config /etc/node-tracker/config.yaml -state /var/lib/node-tracker/state.json\n") ||
		!slices.Equal(withoutExec(got), withoutExec(shipped(t, "system/node-tracker.service"))) {
		t.Errorf("unit:\n%s", got)
	}
	want := []string{
		"useradd --system --user-group --no-create-home --shell /usr/sbin/nologin node-tracker",
		"systemctl daemon-reload",
		"systemctl enable --now node-tracker.service",
		"systemctl status --no-pager node-tracker.service",
	}
	if !slices.Equal(e.cmd.calls, want) {
		t.Errorf("calls = %q", e.cmd.calls)
	}

	// A changed config needs -force too; without -config the installed one is used.
	write(t, cfg, read(t, src)+"# edited\n", 0o600)
	e.cmd.calls = nil
	if err := Install(context.Background(), e.h, Options{System: true, Config: src}); err == nil || len(e.cmd.calls) != 0 {
		t.Fatalf("changed config: err = %v, calls = %q", err, e.cmd.calls)
	}
	if err := Install(context.Background(), e.h, Options{System: true}); err != nil {
		t.Fatal(err)
	}
	if mode(t, cfg) != 0o640 || !strings.HasSuffix(read(t, cfg), "# edited\n") {
		t.Error("installed config: wrong mode or content")
	}
	if !slices.Equal(e.cmd.calls, want[1:]) {
		t.Errorf("calls = %q", e.cmd.calls)
	}
}

// -system detects the node as the service account will: the system cookie,
// not the one in the installing user's home or $BITCOIN_DATADIR.
func TestInstallSystemDetectsAsService(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BITCOIN_DATADIR", filepath.Join(home, "btc"))
	for _, d := range []string{".bitcoin", "btc"} {
		mkdir(t, filepath.Join(home, d), 0o750)
		write(t, filepath.Join(home, d, ".cookie"), "__cookie__:x", 0o640)
	}
	e.h.ServiceEnv = config.ServiceEnv()
	e.h.ServiceEnv.Root = root
	src := filepath.Join(e.conf, "node-tracker", "config.yaml")
	write(t, src, strings.Replace(testConfig(filepath.Join(e.h.EtcDir, "token"), e.cookie),
		`    node: {kind: bitcoind, url: "http://127.0.0.1:8332", cookie_file: `+e.cookie+"}\n", "", 1), 0o600)
	sys := filepath.Join(root, "var", "lib", "bitcoind", ".cookie")
	install := func() error {
		return Install(context.Background(), e.h, Options{System: true, DryRun: true, Config: src})
	}
	if err := install(); err == nil || !strings.Contains(err.Error(), "cookie_file "+sys+" does not exist") {
		t.Fatalf("err = %v, want the system cookie", err)
	}
	mkdir(t, filepath.Dir(sys), 0o750)
	write(t, sys, "__cookie__:x", 0o640)
	if err := install(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallSystemDryRun(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	e.h.Euid = 1000
	e.h.Owner = func() (int, int, error) { return 0, 0, user.UnknownUserError(Account) }
	if err := Install(context.Background(), e.h, Options{System: true, Timer: true, DryRun: true, Config: filepath.Join(e.conf, "node-tracker", "config.yaml")}); err != nil {
		t.Fatal(err)
	}
	if len(e.cmd.calls) != 0 {
		t.Fatalf("calls = %q", e.cmd.calls)
	}
	if fis, _ := os.ReadDir(e.h.SystemUnitDir); len(fis) != 0 || mode(t, filepath.Join(e.h.EtcDir, "token")) != 0o600 {
		t.Fatal("dry run changed files")
	}
	out := e.out.String()
	for _, s := range []string{
		"would run: useradd --system",
		"would set " + e.h.EtcDir + " to 0750 root:node-tracker\n",
		"would write " + filepath.Join(e.h.EtcDir, "config.yaml") + " (0640 root:node-tracker):\n",
		"would set " + filepath.Join(e.h.EtcDir, "token") + " to 0640 root:node-tracker\n",
		"would write " + filepath.Join(e.h.SystemUnitDir, "node-tracker-once.timer") + " (0644):\n",
		"would run: systemctl enable --now node-tracker-once.timer\n",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in\n%s", s, out)
		}
	}
}

func TestInstallSystemRefusals(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	src := filepath.Join(e.conf, "node-tracker", "config.yaml")
	if err := Install(context.Background(), e.h, Options{System: true, Config: src}); os.Geteuid() != 0 && !errors.As(err, new(UsageError)) {
		t.Errorf("not root: err = %v", err)
	}
	e.h.Euid = 0
	if err := Install(context.Background(), e.h, Options{System: true}); err == nil || !strings.Contains(err.Error(), "-config") {
		t.Errorf("no config: err = %v", err)
	}
	outside := filepath.Join(e.conf, "token")
	write(t, outside, "TOKEN", 0o600)
	write(t, src, testConfig(outside, e.cookie), 0o600)
	if err := Install(context.Background(), e.h, Options{System: true, Config: src}); !errors.As(err, new(UsageError)) || !strings.Contains(err.Error(), outside) {
		t.Errorf("secret outside %s: err = %v", e.h.EtcDir, err)
	}
	if len(e.cmd.calls) != 0 {
		t.Errorf("calls = %q", e.cmd.calls)
	}
	if _, err := os.Stat(filepath.Join(e.h.EtcDir, "config.yaml")); err == nil {
		t.Error("config installed despite a refusal")
	}
}

// A blocking directory is opened by the bits that blockers used: the
// account's own group need not be the cookie's.
func TestDirFix(t *testing.T) {
	for _, tt := range []struct {
		x   blocker
		fix string
	}{
		{blocker{path: "/data", uid: 0, gid: 50}, "sudo chmod g+x /data"}, // a group of the account, not the cookie's
		{blocker{path: "/data", uid: 0, gid: 60}, "sudo chmod o+x /data"}, // no group of the account
		{blocker{path: "/data", uid: 7, gid: 50}, "sudo chmod u+x /data"}, // the account's own
	} {
		if _, fix := dirFix(tt.x, 7, []int{50, 1000}); fix != tt.fix {
			t.Errorf("%+v: %q, want %q", tt.x, fix, tt.fix)
		}
	}
}

func TestInstallSystemCookie(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	e.h.Euid = 0
	src := filepath.Join(e.conf, "node-tracker", "config.yaml")
	token := filepath.Join(e.h.EtcDir, "token")
	install := func(cookie string) error {
		write(t, src, testConfig(token, cookie), 0o600)
		return Install(context.Background(), e.h, Options{System: true, Config: src, DryRun: true})
	}

	// Another uid without the cookie's group: refused, with the hint.
	e.h.AccountIDs = func() (int, []int, error) { return 99999, nil, nil }
	if err := install(e.cookie); !errors.As(err, new(UsageError)) || !strings.Contains(err.Error(), "1. sudo usermod -aG ") {
		t.Fatalf("unreadable cookie: err = %v", err)
	}
	// The group from a drop-in, or the account's own groups, let it in.
	dropIn(t, e, "node-tracker.service", "[Service]\nSupplementaryGroups=bitcoin\n")
	if err := install(e.cookie); err != nil {
		t.Fatalf("drop-in group: %v", err)
	}
	dropIn(t, e, "node-tracker.service", "[Service]\nSupplementaryGroups=bitcoin\nSupplementaryGroups=\n")
	if err := install(e.cookie); err == nil {
		t.Fatal("an empty SupplementaryGroups= must reset the list")
	}
	e.h.AccountIDs = func() (int, []int, error) { return 99999, []int{os.Getgid()}, nil }
	if err := install(e.cookie); err != nil {
		t.Fatalf("account group: %v", err)
	}
	// Parent directories need x for it too.
	mkdir(t, filepath.Dir(e.cookie), 0o700)
	if err := install(e.cookie); err == nil || !strings.Contains(err.Error(), "sudo chmod g+x "+filepath.Dir(e.cookie)) {
		t.Fatalf("closed directory: err = %v", err)
	}
	// As on orendil: not in the group, a 0600 cookie in a 0700 datadir. The
	// group is named, and every path that blocks even in it is listed.
	os.Chmod(e.cookie, 0o600)
	e.h.AccountIDs = func() (int, []int, error) { return 99999, nil, nil }
	g, u, dir := groupName(os.Getgid()), userName(os.Getuid()), filepath.Dir(e.cookie)
	pad := strings.Repeat(" ", len(e.cookie)-len(dir))
	want := "the service can't read bitcoind's cookie " + e.cookie + ".\n" +
		"It runs as user node-tracker, which is not in the cookie's group " + g + ", and:\n" +
		"  " + e.cookie + "  owner " + u + ", group " + g + ", mode 0600: its group can't read it\n" +
		"  " + dir + pad + "  owner " + u + ", group " + g + ", mode 0700: its group can't open it\n" +
		"To fix it:\n" +
		"  1. sudo usermod -aG " + g + " node-tracker\n" +
		"  2. add rpccookieperms=group to bitcoin.conf (Bitcoin Core 28 or newer), then restart bitcoind\n" +
		"  3. sudo chmod g+x " + dir
	if err := install(e.cookie); err == nil || err.Error() != want {
		t.Errorf("got\n%v\nwant\n%s", err, want)
	}
	// No account yet (tarball, go install): created first, as install-service
	// would, so that usermod works.
	e.h.AccountIDs = func() (int, []int, error) { return 0, nil, user.UnknownUserError(Account) }
	if err := install(e.cookie); err == nil || !strings.Contains(err.Error(), "It will run as user node-tracker (not created yet), which is not in the cookie's group "+g+", and:\n") ||
		!strings.Contains(err.Error(), "  1. sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin node-tracker\n  2. sudo usermod -aG "+g+" node-tracker\n") {
		t.Errorf("no account: err = %v", err)
	}
	// In the group already: only the paths.
	e.h.AccountIDs = func() (int, []int, error) { return 99999, []int{os.Getgid()}, nil }
	if err := install(e.cookie); err == nil || strings.Contains(err.Error(), "usermod") || !strings.Contains(err.Error(), "which is in the cookie's group "+g+", but:\n") ||
		!strings.HasSuffix(err.Error(), "\n  2. sudo chmod g+x "+dir) {
		t.Errorf("in the group: err = %v", err)
	}
	os.Chmod(e.cookie, 0o640)
	mkdir(t, filepath.Dir(e.cookie), 0o750)

	if err := install(filepath.Join(filepath.Dir(e.cookie), "missing")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing cookie: err = %v", err)
	}
	if err := install("/home/u/.bitcoin/.cookie"); err == nil || !strings.Contains(err.Error(), "ProtectHome") {
		t.Errorf("cookie in /home: err = %v", err)
	}
	// A symlink outside that leads into a protected dir is hidden too.
	home := filepath.Join(t.TempDir(), "home")
	mkdir(t, home, 0o755)
	write(t, filepath.Join(home, ".cookie"), "__cookie__:x", 0o644)
	link := filepath.Join(filepath.Dir(e.cookie), "linked")
	if err := os.Symlink(filepath.Join(home, ".cookie"), link); err != nil {
		t.Fatal(err)
	}
	defer func(old []string) { protectedHome = old }(protectedHome)
	protectedHome = append(slices.Clone(protectedHome), home)
	if err := install(link); err == nil || !strings.Contains(err.Error(), "ProtectHome") {
		t.Errorf("symlink into a protected dir: err = %v", err)
	}
}

func TestInstallSystemPackaged(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	e.h.Euid = 0
	pkg := e.h.PackagedDirs[true]
	mkdir(t, pkg, 0o755)
	for _, n := range []string{"node-tracker-once.service", "node-tracker-once.timer"} {
		b := []byte(shipped(t, "system/"+n))
		if strings.HasSuffix(n, ".service") {
			var err error
			if b, err = render(b, e.exe, nil); err != nil {
				t.Fatal(err)
			}
		}
		write(t, filepath.Join(pkg, n), string(b), 0o644)
	}
	src := filepath.Join(e.conf, "node-tracker", "config.yaml")
	if err := Install(context.Background(), e.h, Options{System: true, Timer: true, Config: src}); err != nil {
		t.Fatalf("%v\n%s", err, e.out)
	}
	if fis, _ := os.ReadDir(e.h.SystemUnitDir); len(fis) != 0 {
		t.Fatalf("wrote units next to the packaged ones: %v", fis)
	}
	if mode(t, filepath.Join(e.h.EtcDir, "config.yaml")) != 0o640 || !slices.Contains(e.cmd.calls, "systemctl enable --now node-tracker-once.timer") {
		t.Fatalf("config not installed or timer not enabled: %q", e.cmd.calls)
	}
}

// A secret from the environment is only a warning, which names the drop-in
// that can supply it: the unit doesn't get the shell's environment.
func TestEnvSecretWarning(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	t.Setenv("NT_TEST_TOKEN", "123:abc")
	src := filepath.Join(e.conf, "node-tracker", "config.yaml")
	write(t, src, strings.Replace(testConfig("x", e.cookie), "bot_token_file: x", "bot_token_env: NT_TEST_TOKEN", 1), 0o600)
	for _, system := range []bool{false, true} {
		e.out.Reset()
		if err := Install(context.Background(), e.h, Options{System: system, DryRun: true, Config: src}); err != nil {
			t.Fatalf("system=%v: %v", system, err)
		}
		edit := map[bool]string{false: "systemctl --user edit node-tracker", true: "systemctl edit node-tracker"}[system]
		if want := "notifiers[0].bot_token_env: the service does not get this shell's environment; use bot_token_file, or set the variable in a drop-in (" + edit + ": Environment= or EnvironmentFile=)"; !strings.Contains(e.out.String(), want) {
			t.Errorf("system=%v: no %q in\n%s", system, want, e.out.String())
		}
	}
}

// fakeTorControl serves PROTOCOLINFO on a unix socket (mode 0660) and names
// cookie as its cookie file. It returns the socket's path.
func fakeTorControl(t *testing.T, cookie string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nt-svc") // short: sun_path is ~108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	mkdir(t, dir, 0o750)
	sock := filepath.Join(dir, "control")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	os.Chmod(sock, 0o660)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(l, "PROTOCOLINFO") {
						fmt.Fprintf(c, "250-PROTOCOLINFO 1\r\n250-AUTH METHODS=COOKIE,SAFECOOKIE COOKIEFILE=%q\r\n250 OK\r\n", cookie)
					} else {
						fmt.Fprint(c, "250 OK\r\n")
					}
				}
			}()
		}
	}()
	return sock
}

func TestInstallSystemTorControl(t *testing.T) {
	e := newEnv(t)
	rootExe(t, e)
	e.h.Euid = 0
	src := filepath.Join(e.conf, "node-tracker", "config.yaml")
	token := filepath.Join(e.h.EtcDir, "token")
	torCookie := filepath.Join(filepath.Dir(e.cookie), "control.authcookie")
	write(t, torCookie, strings.Repeat("x", 32), 0o640)
	sock := fakeTorControl(t, torCookie)
	install := func(ctl string) error {
		cfg := strings.Replace(testConfig(token, e.cookie), `url: "http://127.0.0.1:8332", cookie_file: `+e.cookie,
			`url: "http://2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion:8332", username: u, password_file: `+token+`, tor_control: "`+ctl+`"`, 1)
		write(t, src, cfg, 0o600)
		return Install(context.Background(), e.h, Options{System: true, Config: src, DryRun: true})
	}
	g := groupName(os.Getgid())

	// Not in the group of the socket: refused, with the fix.
	e.h.AccountIDs = func() (int, []int, error) { return 99999, nil, nil }
	err := install("unix:" + sock)
	if !errors.As(err, new(UsageError)) || !strings.Contains(err.Error(), "the service can't open Tor's control socket "+sock+"; it runs as user node-tracker. To fix it:\n  1. sudo usermod -aG "+g+" node-tracker   (the group can control Tor fully") {
		t.Fatalf("socket: err = %v", err)
	}
	// A drop-in's group lets it in, for the socket and the cookie.
	dropIn(t, e, "node-tracker.service", "[Service]\nSupplementaryGroups=bitcoin\n") // the fake host maps bitcoin to the socket's group
	if err := install("unix:" + sock); err != nil {
		t.Fatalf("drop-in group: %v", err)
	}
	// The cookie must be readable too.
	os.Chmod(torCookie, 0o600)
	if err := install("unix:" + sock); err == nil || !strings.Contains(err.Error(), "can't read Tor's cookie "+torCookie+", not even in its group") {
		t.Fatalf("cookie: err = %v", err)
	}
	os.Chmod(torCookie, 0o640)
	// The timer's service has its own drop-ins.
	e.h.AccountIDs = func() (int, []int, error) { return 99999, nil, nil }
	if err := Install(context.Background(), e.h, Options{System: true, Timer: true, Config: src, DryRun: true}); err == nil || !strings.Contains(err.Error(), "control socket") {
		t.Fatalf("timer without its drop-in: err = %v", err)
	}
	// Tor not running: it can't name the cookie, so install-service stops.
	if err := install("unix:" + sock + ".gone"); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("no socket: err = %v", err)
	}
	if err := install("127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "start Tor first") {
		t.Fatalf("no Tor: err = %v", err)
	}
}
