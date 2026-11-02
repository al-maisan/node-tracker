package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEnv is a machine under a temp dir: root holds /etc and /var, home is
// root/home/u. files maps paths ("~/" for home) to contents; a "!" prefix
// makes the file unreadable.
func fakeEnv(t *testing.T, goos string, vars map[string]string, files map[string]string) Env {
	t.Helper()
	root := t.TempDir()
	e := Env{Root: root, Home: filepath.Join(root, "home", "u"), GOOS: goos, Getenv: func(k string) string { return vars[k] }}
	for p, data := range files {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = filepath.Join(e.Home, rest)
		} else {
			p = e.path(p)
		}
		mode := os.FileMode(0o600)
		if d, ok := strings.CutPrefix(data, "!"); ok {
			data, mode = d, 0
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestDetect(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	const cookie = "__cookie__:x"
	tests := []struct {
		name  string
		goos  string
		vars  map[string]string
		files map[string]string
		want  Detected // paths: "~/" for home, "/" for root
	}{
		{"nothing", "linux", nil, nil,
			Detected{"", ChainMain, "http://127.0.0.1:8332", "~/.bitcoin/.cookie"}},
		{"home cookie, no conf", "linux", nil, map[string]string{"~/.bitcoin/.cookie": cookie},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "~/.bitcoin/.cookie"}},
		{"system cookie", "linux", nil, map[string]string{"/var/lib/bitcoind/.cookie": cookie},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "/var/lib/bitcoind/.cookie"}},
		{"unreadable cookie skipped", "linux", nil, map[string]string{"~/.bitcoin/.cookie": "!" + cookie, "/var/lib/bitcoind/.cookie": cookie},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "/var/lib/bitcoind/.cookie"}},
		{"signet", "linux", nil, map[string]string{"~/.bitcoin/bitcoin.conf": "signet=1\n", "~/.bitcoin/signet/.cookie": cookie},
			Detected{"~/.bitcoin/bitcoin.conf", ChainSignet, "http://127.0.0.1:38332", "~/.bitcoin/signet/.cookie"}},
		{"testnet4 by chain=", "linux", nil, map[string]string{"~/.bitcoin/bitcoin.conf": "chain=testnet4\n"},
			Detected{"~/.bitcoin/bitcoin.conf", ChainTestnet4, "http://127.0.0.1:48332", "~/.bitcoin/testnet4/.cookie"}},
		{"testnet3", "linux", nil, map[string]string{"~/.bitcoin/bitcoin.conf": "testnet=1\n"},
			Detected{"~/.bitcoin/bitcoin.conf", ChainTest, "http://127.0.0.1:18332", "~/.bitcoin/testnet3/.cookie"}},
		{"regtest with port in section", "linux", nil, map[string]string{"~/.bitcoin/bitcoin.conf": "regtest=1\n[regtest]\nrpcport=19000\n"},
			Detected{"~/.bitcoin/bitcoin.conf", ChainRegtest, "http://127.0.0.1:19000", "~/.bitcoin/regtest/.cookie"}},
		{"system conf, datadir and cookie file", "linux", nil, map[string]string{
			"/etc/bitcoin/bitcoin.conf": "datadir=/data/btc\nrpccookiefile=/run/bitcoind/cookie\nrpcport=8400\n",
			"/run/bitcoind/cookie":      cookie,
		}, Detected{"/etc/bitcoin/bitcoin.conf", ChainMain, "http://127.0.0.1:8400", "/run/bitcoind/cookie"}},
		{"relative cookie file is in the chain's datadir", "linux", nil, map[string]string{
			"/etc/bitcoin/bitcoin.conf":        "datadir=/data/btc\nsignet=1\nrpccookiefile=auth\n",
			"/data/btc/signet/auth":            cookie,
			"/var/lib/bitcoind/signet/.cookie": cookie,
		}, Detected{"/etc/bitcoin/bitcoin.conf", ChainSignet, "http://127.0.0.1:38332", "/data/btc/signet/auth"}},
		{"conf cookie missing, falls back", "linux", nil, map[string]string{
			"/etc/bitcoin/bitcoin.conf": "rpccookiefile=/nope/.cookie\n",
			"/var/lib/bitcoind/.cookie": cookie,
		}, Detected{"/etc/bitcoin/bitcoin.conf", ChainMain, "http://127.0.0.1:8332", "/var/lib/bitcoind/.cookie"}},
		{"nothing readable: the conf's cookie", "linux", nil, map[string]string{"/etc/bitcoin/bitcoin.conf": "rpccookiefile=/nope/.cookie\n"},
			Detected{"/etc/bitcoin/bitcoin.conf", ChainMain, "http://127.0.0.1:8332", "/nope/.cookie"}},
		{"unreadable conf skipped", "linux", nil, map[string]string{"~/.bitcoin/bitcoin.conf": "!signet=1\n", "/etc/bitcoin/bitcoin.conf": "testnet4=1\n"},
			Detected{"/etc/bitcoin/bitcoin.conf", ChainTestnet4, "http://127.0.0.1:48332", "/var/lib/bitcoind/testnet4/.cookie"}},
		{"BITCOIN_DATADIR first", "linux", map[string]string{"BITCOIN_DATADIR": "/mnt/ssd/bitcoin"}, map[string]string{
			"/mnt/ssd/bitcoin/bitcoin.conf": "signet=1\n", "~/.bitcoin/bitcoin.conf": "regtest=1\n",
		}, Detected{"/mnt/ssd/bitcoin/bitcoin.conf", ChainSignet, "http://127.0.0.1:38332", "/mnt/ssd/bitcoin/signet/.cookie"}},
		{"macOS", "darwin", nil, map[string]string{"~/Library/Application Support/Bitcoin/.cookie": cookie},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "~/Library/Application Support/Bitcoin/.cookie"}},
		{"macOS path only on macOS", "linux", nil, map[string]string{"~/Library/Application Support/Bitcoin/.cookie": cookie},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "~/.bitcoin/.cookie"}},
		{"snap", "linux", nil, map[string]string{"~/snap/bitcoin-core/common/.bitcoin/bitcoin.conf": "chain=signet\n"},
			Detected{"~/snap/bitcoin-core/common/.bitcoin/bitcoin.conf", ChainSignet, "http://127.0.0.1:38332", "~/snap/bitcoin-core/common/.bitcoin/signet/.cookie"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := fakeEnv(t, tt.goos, tt.vars, tt.files)
			fix := func(p string) string {
				if rest, ok := strings.CutPrefix(p, "~/"); ok {
					return filepath.Join(e.Home, rest)
				}
				if p == "" {
					return ""
				}
				return e.path(p)
			}
			want := tt.want
			want.Conf, want.CookieFile = fix(want.Conf), fix(want.CookieFile)
			if got := e.Detect(); got != want {
				t.Fatalf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

// A running bitcoind's arguments win over bitcoin.conf, as in Core.
func TestDetectArgs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	const cookie = "__cookie__:x"
	tests := []struct {
		name  string
		args  []string
		files map[string]string
		want  Detected // paths: "~/" for home (/home/u), "/" for root
	}{
		{"-conf in the home, as on orendil", []string{"-conf=/home/u/.bitcoin/bitcoin.conf"},
			map[string]string{"~/.bitcoin/bitcoin.conf": "server=1\n", "~/.bitcoin/.cookie": cookie, "/var/lib/bitcoind/.cookie": cookie},
			Detected{"~/.bitcoin/bitcoin.conf", ChainMain, "http://127.0.0.1:8332", "~/.bitcoin/.cookie"}},
		{"-conf elsewhere, datadir in it", []string{"-conf=/opt/btc/bitcoin.conf"},
			map[string]string{"/opt/btc/bitcoin.conf": "datadir=/data/btc\n", "/data/btc/.cookie": cookie, "~/.bitcoin/.cookie": cookie},
			Detected{"/opt/btc/bitcoin.conf", ChainMain, "http://127.0.0.1:8332", "/data/btc/.cookie"}},
		{"-datadir, its bitcoin.conf", []string{"-datadir=/mnt/ssd/bitcoin"},
			map[string]string{"/mnt/ssd/bitcoin/bitcoin.conf": "signet=1\n", "/mnt/ssd/bitcoin/signet/.cookie": cookie, "~/.bitcoin/bitcoin.conf": "regtest=1\n"},
			Detected{"/mnt/ssd/bitcoin/bitcoin.conf", ChainSignet, "http://127.0.0.1:38332", "/mnt/ssd/bitcoin/signet/.cookie"}},
		{"-datadir wins over the conf's datadir", []string{"--datadir=/a", "-conf=/etc/x.conf"},
			map[string]string{"/etc/x.conf": "datadir=/b\n", "/a/.cookie": cookie, "/b/.cookie": cookie},
			Detected{"/etc/x.conf", ChainMain, "http://127.0.0.1:8332", "/a/.cookie"}},
		{"relative -conf is in -datadir", []string{"-datadir=/d", "-conf=my.conf"},
			map[string]string{"/d/my.conf": "chain=testnet4\n", "/d/testnet4/.cookie": cookie},
			Detected{"/d/my.conf", ChainTestnet4, "http://127.0.0.1:48332", "/d/testnet4/.cookie"}},
		{"the chain of the arguments wins, with its section", []string{"-datadir=/d", "-testnet4"},
			map[string]string{"/d/bitcoin.conf": "chain=signet\n[testnet4]\nrpcport=1234\n", "/d/testnet4/.cookie": cookie},
			Detected{"/d/bitcoin.conf", ChainTestnet4, "http://127.0.0.1:1234", "/d/testnet4/.cookie"}},
		{"-rpcport and -rpccookiefile win", []string{"-datadir=/d", "-rpcport=9000", "-rpccookiefile=/run/c"},
			map[string]string{"/d/bitcoin.conf": "rpcport=8400\nrpccookiefile=/run/other\n", "/run/c": cookie, "/run/other": cookie},
			Detected{"/d/bitcoin.conf", ChainMain, "http://127.0.0.1:9000", "/run/c"}},
		{"-rpccookiefile without a conf", []string{"-rpccookiefile=/run/c", "-regtest"},
			map[string]string{"/run/c": cookie},
			Detected{"", ChainRegtest, "http://127.0.0.1:18443", "/run/c"}},
		{"-datadir without its conf: no other conf is read", []string{"-datadir=/mnt/ssd"},
			map[string]string{"/etc/bitcoin/bitcoin.conf": "testnet=1\n", "/mnt/ssd/.cookie": cookie},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "/mnt/ssd/.cookie"}},
		{"-conf missing: no other cookie", []string{"-conf=/x.conf"},
			map[string]string{"/var/lib/bitcoind/.cookie": cookie, "/etc/bitcoin/bitcoin.conf": "signet=1\n"},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "~/.bitcoin/.cookie"}},
		{"relative paths only: as without arguments", []string{"-conf=x.conf", "-datadir=rel", "-daemon"},
			map[string]string{"~/.bitcoin/.cookie": cookie},
			Detected{"", ChainMain, "http://127.0.0.1:8332", "~/.bitcoin/.cookie"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := fakeEnv(t, "linux", nil, tt.files)
			e.Args = tt.args
			fix := func(p string) string {
				if rest, ok := strings.CutPrefix(p, "~/"); ok {
					return filepath.Join(e.Home, rest)
				}
				if p == "" {
					return ""
				}
				return e.path(p)
			}
			want := tt.want
			want.Conf, want.CookieFile = fix(want.Conf), fix(want.CookieFile)
			if got := e.Detect(); got != want {
				t.Fatalf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	got := parseArgs([]string{"-conf=/a", "--datadir=/b", "-testnet", "-rpcport=1", "-rpcport=2", "notanoption", "-x=y=z"})
	want := map[string]string{"conf": "/a", "datadir": "/b", "testnet": "", "rpcport": "2", "x": "y=z"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Without a home, as for the system service, only system places are tried.
func TestDetectWithoutHome(t *testing.T) {
	e := fakeEnv(t, "darwin", nil, map[string]string{
		"~/.bitcoin/bitcoin.conf":                       "signet=1\n",
		"~/.bitcoin/.cookie":                            "c",
		"~/Library/Application Support/Bitcoin/.cookie": "c",
	})
	e.Home, e.Getenv = "", nil
	want := Detected{"", ChainMain, "http://127.0.0.1:8332", e.path("/var/lib/bitcoind/.cookie")}
	if got := e.Detect(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSystemAndServiceEnv(t *testing.T) {
	t.Setenv("BITCOIN_DATADIR", "/mnt/btc")
	for _, home := range []string{"/", ""} {
		t.Setenv("HOME", home)
		if e := SystemEnv(); e.Home != "" || e.Root != "/" {
			t.Errorf("HOME=%q: %+v", home, e)
		}
	}
	t.Setenv("HOME", "/home/u")
	if e := SystemEnv(); e.Home != "/home/u" || e.getenv("BITCOIN_DATADIR") != "/mnt/btc" {
		t.Errorf("SystemEnv = %+v", e)
	}
	if e := ServiceEnv(); e.Home != "" || e.Root != "/" || e.getenv("BITCOIN_DATADIR") != "" {
		t.Errorf("ServiceEnv = %+v", e)
	}
}

func TestParseBitcoinConf(t *testing.T) {
	tests := []struct {
		name, conf string
		want       bitcoinConf
	}{
		{"empty", "", bitcoinConf{chain: ChainMain}},
		{"comments and spaces", "  rpcport = 8400  # mine\n# rpcport=1\n", bitcoinConf{chain: ChainMain, rpcport: "8400"}},
		{"last wins", "rpcport=1\nrpcport=2\n", bitcoinConf{chain: ChainMain, rpcport: "2"}},
		{"bad port", "rpcport=http\n", bitcoinConf{chain: ChainMain}},
		{"port out of range", "rpcport=70000\n", bitcoinConf{chain: ChainMain}},
		{"top-level port is mainnet only", "signet=1\nrpcport=8400\n", bitcoinConf{chain: ChainSignet}},
		{"section port", "testnet=1\n[test]\nrpcport=18400\n[main]\nrpcport=8400\n", bitcoinConf{chain: ChainTest, rpcport: "18400"}},
		{"dotted key", "chain=regtest\nregtest.rpcport=19000\nmain.rpcport=8400\n", bitcoinConf{chain: ChainRegtest, rpcport: "19000"}},
		{"chain in a section is ignored", "[main]\nsignet=1\n", bitcoinConf{chain: ChainMain}},
		{"flag off", "testnet=0\n", bitcoinConf{chain: ChainMain}},
		{"empty flag is on", "signet=\n", bitcoinConf{chain: ChainSignet}},
		{"unknown chain", "chain=litecoin\n", bitcoinConf{chain: ChainMain}},
		{"cookie file, top level for every chain", "signet=1\nrpccookiefile=/c\ndatadir=/d\n", bitcoinConf{chain: ChainSignet, cookieFile: "/c", datadir: "/d"}},
		{"cookie file in section wins", "signet=1\nrpccookiefile=/c\n[signet]\nrpccookiefile=/s\n", bitcoinConf{chain: ChainSignet, cookieFile: "/s"}},
		{"unknown keys", "server=1\nrpcuser=u\nrpcpassword=hunter2\nfoo\n[weird]\n", bitcoinConf{chain: ChainMain}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBitcoinConf([]byte(tt.conf), "")
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if strings.Contains(fmt.Sprintf("%+v", got), "hunter2") {
				t.Fatal("password kept")
			}
		})
	}
}

func TestConfBool(t *testing.T) {
	for v, want := range map[string]bool{"": true, "1": true, "0": false, "2": true, "-1": true, "1x": true, "true": false, "00": false} {
		if confBool(v) != want {
			t.Errorf("confBool(%q) = %v", v, !want)
		}
	}
}

func TestReferenceChain(t *testing.T) {
	for _, tt := range []struct{ kind, url, want string }{
		{KindEsplora, "https://mempool.space/api", ChainMain},
		{KindEsplora, "https://blockstream.info/api/", ChainMain},
		{KindEsplora, "https://www.Mempool.Space/api", ChainMain},
		{KindEsplora, "https://mempool.space/testnet/api", ChainTest},
		{KindEsplora, "https://mempool.space/testnet4/api", ChainTestnet4},
		{KindEsplora, "https://mempool.space/signet/api", ChainSignet},
		{KindEsplora, "https://blockstream.info/testnet/api", ChainTest},
		{KindEsplora, "http://127.0.0.1:3002/api", ""},
		{KindEsplora, "https://notmempool.space/api", ""},
		{KindBitcoind, "https://mempool.space/api", ""},
	} {
		if got := ReferenceChain(Endpoint{Kind: tt.kind, URL: tt.url}); got != tt.want {
			t.Errorf("%s %s: %q, want %q", tt.kind, tt.url, got, tt.want)
		}
	}
	for chain, urls := range defaultReferences { // the defaults are recognized
		for _, u := range urls {
			if got := ReferenceChain(Endpoint{Kind: KindEsplora, URL: u}); got != chain {
				t.Errorf("default %s: %q, want %q", u, got, chain)
			}
		}
	}
}

// minimal is a config with nothing but a target name and a notifier.
const minimal = `
targets:
  - name: orendil
notifiers:
  - name: ops
    kind: telegram
    bot_token_file: TOKENFILE
    chat_id: "42"
`

func TestDefaults(t *testing.T) {
	const cookie = "__cookie__:x"
	tests := []struct {
		name      string
		cfg       string
		files     map[string]string
		wantNode  string // url
		wantRefs  []string
		wantWarns int
		detected  bool
	}{
		{"mainnet", minimal, map[string]string{"~/.bitcoin/.cookie": cookie}, "http://127.0.0.1:8332",
			[]string{"https://mempool.space/api", "https://blockstream.info/api"}, 0, true},
		{"signet", minimal, map[string]string{"~/.bitcoin/bitcoin.conf": "signet=1", "~/.bitcoin/signet/.cookie": cookie}, "http://127.0.0.1:38332",
			[]string{"https://mempool.space/signet/api"}, 0, true},
		{"regtest: no references", minimal, map[string]string{"~/.bitcoin/bitcoin.conf": "regtest=1", "~/.bitcoin/regtest/.cookie": cookie}, "http://127.0.0.1:18443",
			nil, 1, true},
		{"no cookie: warning", minimal, nil, "http://127.0.0.1:8332",
			[]string{"https://mempool.space/api", "https://blockstream.info/api"}, 1, true},
		{"references: [] means none", strings.Replace(minimal, "  - name: orendil\n", "  - name: orendil\n    references: []\n", 1),
			map[string]string{"~/.bitcoin/.cookie": cookie}, "http://127.0.0.1:8332", nil, 1, true},
		{"explicit references", strings.Replace(minimal, "  - name: orendil\n", "  - name: orendil\n    references: [{kind: esplora, url: 'https://example.com/api'}]\n", 1),
			map[string]string{"~/.bitcoin/.cookie": cookie}, "http://127.0.0.1:8332", []string{"https://example.com/api"}, 0, true},
		{"explicit node: references by its port", strings.Replace(minimal, "  - name: orendil\n", "  - name: orendil\n    node: {kind: bitcoind, url: 'http://127.0.0.1:48332', cookie_file: COOKIEFILE}\n", 1),
			map[string]string{"~/.bitcoin/bitcoin.conf": "signet=1"}, "http://127.0.0.1:48332", []string{"https://mempool.space/testnet4/api"}, 0, false},
		{"explicit node: other port is mainnet", strings.Replace(minimal, "  - name: orendil\n", "  - name: orendil\n    node: {kind: bitcoind, url: 'http://127.0.0.1:9000', cookie_file: COOKIEFILE}\n", 1),
			nil, "http://127.0.0.1:9000", []string{"https://mempool.space/api", "https://blockstream.info/api"}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := fakeEnv(t, "linux", nil, tt.files)
			c, warns, err := LoadEnv(write(t, tt.cfg), env)
			if err != nil {
				t.Fatal(err)
			}
			tg := c.Targets[0]
			var refs []string
			for _, r := range tg.References {
				refs = append(refs, r.URL)
			}
			if tg.Node.URL != tt.wantNode || tg.Node.Kind != KindBitcoind || fmt.Sprint(refs) != fmt.Sprint(tt.wantRefs) || len(warns) != tt.wantWarns {
				t.Fatalf("node=%+v refs=%v warns=%q", tg.Node, refs, warns)
			}
			if tg.DefaultRefs == strings.Contains(tt.cfg, "references:") {
				t.Fatalf("DefaultRefs = %v", tg.DefaultRefs)
			}
			if tt.detected {
				if c.Detected == nil || tg.Node.CookieFile != c.Detected.CookieFile || tg.Node.URL != c.Detected.URL || tg.Node.Name != "bitcoind:"+strings.TrimPrefix(tt.wantNode, "http://") {
					t.Fatalf("detected=%+v node=%+v", c.Detected, tg.Node)
				}
			} else if c.Detected != nil {
				t.Fatalf("detected without need: %+v", c.Detected)
			}
			if a := tg.Alert; *a.Threshold != 3 || *a.RecoverThreshold != 1 || a.For != 2 {
				t.Fatalf("alert = %d/%d/%d, want the balanced preset", *a.Threshold, *a.RecoverThreshold, a.For)
			}
			if fmt.Sprint(tg.Notify) != "[ops]" {
				t.Fatalf("notify = %v", tg.Notify)
			}
		})
	}
}

// Defaults fill in what is left out; explicit values stay.
func TestDefaultsPartial(t *testing.T) {
	env := fakeEnv(t, "linux", nil, map[string]string{"~/.bitcoin/.cookie": "__cookie__:x"})
	two := minimal + "  - {name: second, kind: telegram, bot_token_file: TOKENFILE, chat_id: \"43\"}\n"
	for _, tt := range []struct {
		name, alert            string
		threshold              uint64
		recover, ffor, downFor int
		notify                 string
		wantErr                string
	}{
		{"threshold only", "    alert: {threshold: 6}\n", 6, 6, 2, 2, "[ops second]", ""},
		{"for only", "    alert: {for: 3}\n", 3, 1, 3, 3, "[ops second]", ""},
		{"down_for only", "    alert: {down_for: 10}\n", 3, 1, 2, 10, "[ops second]", ""},
		{"down_for too large", "    alert: {down_for: 101}\n", 0, 0, 0, 0, "", "down_for: must be between 1 and 100"},
		{"recover only", "    alert: {recover_threshold: 2}\n", 3, 2, 2, 2, "[ops second]", ""},
		{"recover above default", "    alert: {recover_threshold: 4}\n", 0, 0, 0, 0, "", "recover_threshold: must not exceed threshold (3)"},
		{"explicit notify", "    notify: [second]\n", 3, 1, 2, 2, "[second]", ""},
		{"empty notify", "    notify: []\n", 0, 0, 0, 0, "", "notify: at least one notifier is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _, err := LoadEnv(write(t, strings.Replace(two, "  - name: orendil\n", "  - name: orendil\n"+tt.alert, 1)), env)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			a := c.Targets[0].Alert
			if *a.Threshold != tt.threshold || *a.RecoverThreshold != uint64(tt.recover) || a.For != tt.ffor || a.DownFor != tt.downFor || fmt.Sprint(c.Targets[0].Notify) != tt.notify {
				t.Fatalf("alert = %d/%d/%d/%d notify = %v", *a.Threshold, *a.RecoverThreshold, a.For, a.DownFor, c.Targets[0].Notify)
			}
		})
	}
	t.Run("no notifiers", func(t *testing.T) {
		_, _, err := LoadEnv(write(t, "targets:\n  - name: x\n"), env)
		if err == nil || !strings.Contains(err.Error(), "targets[0].notify: at least one notifier is required") {
			t.Fatalf("err = %v", err)
		}
	})
}

// The minimal config in the docs stays valid and 8 lines long.
func TestDocsMinimalConfig(t *testing.T) {
	b, err := os.ReadFile("../../docs/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(b), "## Minimal config")
	_, rest, _ = strings.Cut(rest, "```yaml\n")
	cfg, _, ok := strings.Cut(rest, "```")
	if !ok || strings.Count(cfg, "\n") != 8 {
		t.Fatalf("minimal config not found or not 8 lines:\n%s", cfg)
	}
	env := fakeEnv(t, "linux", nil, map[string]string{"~/.bitcoin/.cookie": "__cookie__:x"})
	c, warns, err := LoadEnv(write(t, strings.Replace(cfg, "~/.config/node-tracker/telegram-token", "TOKENFILE", 1)), env)
	if err != nil || len(warns) != 0 || len(c.Targets[0].References) != 2 {
		t.Fatalf("err=%v warns=%q", err, warns)
	}
}
