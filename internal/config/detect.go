package config

import (
	"bufio"
	"bytes"
	"cmp"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/al-maisan/node-tracker/internal/fsx"
)

// Chains, named as getblockchaininfo does.
const (
	ChainMain     = "main"
	ChainTest     = "test" // testnet3
	ChainTestnet4 = "testnet4"
	ChainSignet   = "signet"
	ChainRegtest  = "regtest"
)

// chainInfo is Bitcoin Core's default RPC port and datadir subdirectory of
// each chain.
var chainInfo = map[string]struct{ port, subdir string }{
	ChainMain:     {"8332", ""},
	ChainTest:     {"18332", "testnet3"},
	ChainTestnet4: {"48332", "testnet4"},
	ChainSignet:   {"38332", "signet"},
	ChainRegtest:  {"18443", "regtest"},
}

// defaultReferences are the public Esplora APIs of each chain.
var defaultReferences = map[string][]string{
	ChainMain:     {"https://mempool.space/api", "https://blockstream.info/api"},
	ChainTest:     {"https://mempool.space/testnet/api"},
	ChainTestnet4: {"https://mempool.space/testnet4/api"},
	ChainSignet:   {"https://mempool.space/signet/api"},
	ChainRegtest:  {},
}

// publicEsplora are hosts whose API root without a chain path is mainnet.
var publicEsplora = []string{"mempool.space", "blockstream.info"}

const maxBitcoinConf = 1 << 20

// Env is where the local bitcoind is looked for. Tests use a fake one.
type Env struct {
	Root   string // prefix of absolute paths, "/" for the real system
	Home   string // "" if unknown
	GOOS   string
	Getenv func(string) string // nil: no environment
	// Args are a running bitcoind's arguments, without the program name:
	// as in Core, its -datadir, -conf, chain, -rpcport and -rpccookiefile
	// win over bitcoin.conf.
	Args []string
}

// SystemEnv is the real machine. Without a home (HOME unset or "/", as
// for a system service account), no home-based datadir is tried.
func SystemEnv() Env {
	home, _ := os.UserHomeDir()
	if home == "/" {
		home = ""
	}
	return Env{Root: "/", Home: home, GOOS: runtime.GOOS, Getenv: os.Getenv}
}

// ServiceEnv is the real machine as the system service sees it: only the
// system datadir and config, nothing from this user's home or environment.
func ServiceEnv() Env {
	return Env{Root: "/", GOOS: runtime.GOOS, Getenv: func(string) string { return "" }}
}

// Detected is the local bitcoind as found from bitcoin.conf and the
// known datadirs.
type Detected struct {
	Conf       string // the bitcoin.conf read, "" if none
	Chain      string
	URL        string
	CookieFile string // the first readable cookie, else the most likely path
}

// conf is a bitcoin.conf and the datadir it belongs to.
type conf struct{ path, datadir string }

// datadirs are the known datadirs, most specific first, each with the
// bitcoin.conf to try for it.
func (e Env) datadirs() []conf {
	var cs []conf
	add := func(dir, path string) {
		if dir != "" {
			cs = append(cs, conf{path, dir})
		}
	}
	// As in Core, a relative -conf is in the datadir, and without -datadir
	// that is the default one. Only that conf is read then, and only that
	// datadir has the cookie.
	args := parseArgs(e.Args)
	if d, c := args["datadir"], args["conf"]; filepath.IsAbs(d) || filepath.IsAbs(c) {
		switch {
		case filepath.IsAbs(d):
			d = e.path(d)
		case e.Home != "" && e.GOOS == "darwin":
			d = filepath.Join(e.Home, "Library", "Application Support", "Bitcoin")
		case e.Home != "":
			d = filepath.Join(e.Home, ".bitcoin")
		default:
			d = e.path("/var/lib/bitcoind")
		}
		if filepath.IsAbs(c) {
			c = e.path(c)
		} else {
			c = filepath.Join(d, cmp.Or(c, "bitcoin.conf"))
		}
		return []conf{{c, d}}
	}
	if d := e.getenv("BITCOIN_DATADIR"); filepath.IsAbs(d) {
		add(e.path(d), filepath.Join(e.path(d), "bitcoin.conf"))
	}
	if e.Home != "" {
		home := []string{".bitcoin"}
		if e.GOOS == "darwin" {
			home = append(home, filepath.Join("Library", "Application Support", "Bitcoin"))
		}
		for _, h := range append(home, filepath.Join("snap", "bitcoin-core", "common", ".bitcoin")) {
			d := filepath.Join(e.Home, h)
			add(d, filepath.Join(d, "bitcoin.conf"))
		}
	}
	// Bitcoin Core's contrib/init units: -conf=/etc/bitcoin/bitcoin.conf
	// -datadir=/var/lib/bitcoind.
	sys := e.path("/var/lib/bitcoind")
	add(sys, e.path("/etc/bitcoin/bitcoin.conf"))
	add(sys, filepath.Join(sys, "bitcoin.conf"))
	return cs
}

func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

// path is the absolute path p under e.Root.
func (e Env) path(p string) string { return filepath.Join(e.Root, p) }

// Detect finds the local bitcoind: the first readable bitcoin.conf of the
// known places gives the chain, the RPC port and the cookie path. The
// cookie is the first readable one of that and the known datadirs. Only
// chain, testnet, testnet4, signet, regtest, datadir, rpcport and
// rpccookiefile are read from bitcoin.conf, and from e.Args, which win;
// credentials never are.
func (e Env) Detect() Detected {
	args := parseArgs(e.Args)
	argChain := chainOf(func(k string) (string, bool) { v, ok := args[k]; return v, ok })
	d := Detected{Chain: cmp.Or(argChain, ChainMain)}
	dirs := e.datadirs()
	var datadir, cookieFile, rpcport string
	if c, b := firstReadable(dirs); b != nil {
		bc := parseBitcoinConf(b, argChain)
		d.Conf, d.Chain = c.path, bc.chain
		datadir, cookieFile, rpcport = c.datadir, bc.cookieFile, bc.rpcport
		if filepath.IsAbs(bc.datadir) {
			datadir = e.path(bc.datadir)
		}
	}
	if v := args["datadir"]; filepath.IsAbs(v) {
		datadir = e.path(v)
	}
	if v, ok := args["rpccookiefile"]; ok {
		cookieFile = v
	}
	if v := args["rpcport"]; validPort(v) {
		rpcport = v
	}
	var cookies []string
	switch {
	case filepath.IsAbs(cookieFile):
		cookies = append(cookies, e.path(cookieFile))
	case datadir == "":
	case cookieFile != "": // relative to the chain's datadir, as in Core
		cookies = append(cookies, filepath.Join(datadir, chainInfo[d.Chain].subdir, cookieFile))
	default:
		cookies = append(cookies, filepath.Join(datadir, chainInfo[d.Chain].subdir, ".cookie"))
	}
	d.URL = "http://127.0.0.1:" + cmp.Or(rpcport, chainInfo[d.Chain].port)
	for _, c := range dirs {
		cookies = append(cookies, filepath.Join(c.datadir, chainInfo[d.Chain].subdir, ".cookie"))
	}
	d.CookieFile = cookies[0]
	for _, c := range cookies { // in a datadir bitcoind's user controls; setup may run as root
		if f, err := fsx.OpenRegular(c); err == nil {
			f.Close()
			d.CookieFile = c
			break
		}
	}
	return d
}

// firstReadable is the first conf of cs that can be read, and its content.
func firstReadable(cs []conf) (conf, []byte) {
	for _, c := range cs {
		if b, err := readSmall(c.path); err == nil {
			return c, b
		}
	}
	return conf{}, nil
}

func readSmall(path string) ([]byte, error) {
	f, err := fsx.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxBitcoinConf))
}

type bitcoinConf struct{ chain, datadir, rpcport, cookieFile string }

// parseBitcoinConf reads the keys Detect needs, as Bitcoin Core does:
// key=value lines, # comments, [main]/[test]/[testnet4]/[signet]/[regtest]
// sections and chain.key=value. The chain is selected at the top level,
// unless chain is given;
// rpcport counts at the top level for mainnet only, rpccookiefile for every
// chain. Other keys, rpcpassword included, are skipped unread.
func parseBitcoinConf(b []byte, chain string) bitcoinConf {
	type kv struct{ section, key, value string }
	var kvs []kv
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		l, _, _ := strings.Cut(sc.Text(), "#")
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			section = strings.TrimSpace(l[1 : len(l)-1])
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		s := section
		if pre, key, ok := strings.Cut(k, "."); ok {
			s, k = pre, key
		}
		switch k {
		case "chain", "testnet", "testnet4", "signet", "regtest", "datadir", "rpcport", "rpccookiefile":
			kvs = append(kvs, kv{s, k, v})
		}
	}
	// The last value wins, as for a repeated single-value option in Core.
	get := func(section, key string) (string, bool) {
		v, found := "", false
		for _, e := range kvs {
			if e.section == section && e.key == key {
				v, found = e.value, true
			}
		}
		return v, found
	}
	c := bitcoinConf{chain: cmp.Or(chain, chainOf(func(k string) (string, bool) { return get("", k) }), ChainMain)}
	c.datadir, _ = get("", "datadir")
	if v, ok := get(c.chain, "rpcport"); ok {
		c.rpcport = v
	} else if c.chain == ChainMain {
		c.rpcport, _ = get("", "rpcport")
	}
	if !validPort(c.rpcport) {
		c.rpcport = ""
	}
	if v, ok := get(c.chain, "rpccookiefile"); ok {
		c.cookieFile = v
	} else {
		c.cookieFile, _ = get("", "rpccookiefile")
	}
	return c
}

// chainOf is the chain that the options of get select, "" for none.
func chainOf(get func(key string) (string, bool)) string {
	if v, ok := get("chain"); ok && chainInfo[v].port != "" {
		return v
	}
	for _, f := range []struct{ key, chain string }{{"regtest", ChainRegtest}, {"signet", ChainSignet}, {"testnet4", ChainTestnet4}, {"testnet", ChainTest}} {
		if v, ok := get(f.key); ok && confBool(v) {
			return f.chain
		}
	}
	return ""
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// parseArgs reads bitcoind's arguments: -key=value or --key=value, and
// -key alone as key="" (true for a boolean). The last value wins.
func parseArgs(args []string) map[string]string {
	m := map[string]string{}
	for _, a := range args {
		if k, ok := strings.CutPrefix(a, "-"); ok {
			k, v, _ := strings.Cut(strings.TrimPrefix(k, "-"), "=")
			m[k] = v
		}
	}
	return m
}

// confBool is Bitcoin Core's reading of a boolean option: empty is true,
// else the leading integer is non-zero.
func confBool(v string) bool {
	if v == "" {
		return true
	}
	end := 0
	for end < len(v) && (v[end] >= '0' && v[end] <= '9' || end == 0 && (v[0] == '-' || v[0] == '+')) {
		end++
	}
	n, _ := strconv.Atoi(v[:end])
	return n != 0
}

// DefaultReferences are the public references of chain, none if there are
// none (regtest) or the chain is unknown.
func DefaultReferences(chain string) []Endpoint {
	es := []Endpoint{}
	for _, u := range defaultReferences[chain] {
		es = append(es, Endpoint{Kind: KindEsplora, URL: u})
	}
	return es
}

// portChain is the chain whose default RPC port the URL uses, else main.
func portChain(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		for chain, ci := range chainInfo {
			if u.Port() == ci.port {
				return chain
			}
		}
	}
	return ChainMain
}

// ReferenceChain is the chain of an Esplora reference, from its URL path
// (/testnet, /testnet4, /signet) or, for mempool.space and
// blockstream.info, mainnet without one. It is "" if unknown.
func ReferenceChain(e Endpoint) string {
	if e.Kind != KindEsplora {
		return ""
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return ""
	}
	for _, seg := range strings.Split(u.Path, "/") {
		switch seg {
		case "testnet":
			return ChainTest
		case ChainTestnet4, ChainSignet, ChainRegtest:
			return seg
		}
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range publicEsplora {
		if host == h || strings.HasSuffix(host, "."+h) {
			return ChainMain
		}
	}
	return ""
}
