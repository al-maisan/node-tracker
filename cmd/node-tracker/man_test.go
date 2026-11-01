package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The man page documents every command and every flag, so the two can't
// drift apart.
func TestManPageCoversCLI(t *testing.T) {
	b, err := os.ReadFile("../../node-tracker.1")
	if err != nil {
		t.Fatal(err)
	}
	man := strings.ReplaceAll(string(b), `\-`, "-")

	// The packaging fills these in (scripts/dist.sh, ppa/debian/rules).
	if !strings.Contains(man, `.TH NODE-TRACKER 1 "@DATE@" "node-tracker @VERSION@"`) {
		t.Error(".TH must carry the @DATE@ and @VERSION@ placeholders")
	}

	cmds := regexp.MustCompile(`(?m)^  ([a-z-]+) {2,}`).FindAllStringSubmatch(usage, -1)
	if len(cmds) == 0 {
		t.Fatal("no commands found in usage")
	}
	for _, c := range append(cmds, []string{"", "help"}) {
		if !regexp.MustCompile(`(?m)^\.TP\n\.BR? ` + regexp.QuoteMeta(c[1]) + `\b`).MatchString(man) {
			t.Errorf("command %q is not documented in node-tracker.1", c[1])
		}
	}

	flagRE := regexp.MustCompile(`(?m)^  (-[a-z-]+)`)
	for _, c := range cmds {
		if c[1] == "version" {
			continue
		}
		code, _, stderr := exec(t, c[1], "-h")
		if code != exitOK {
			t.Fatalf("%s -h: exit %d", c[1], code)
		}
		for _, f := range flagRE.FindAllStringSubmatch(stderr, -1) {
			if !regexp.MustCompile(`(?m)^\.TP\n\.BI? ` + regexp.QuoteMeta(f[1]) + `\b`).MatchString(man) {
				t.Errorf("flag %s of %s is not documented in node-tracker.1", f[1], c[1])
			}
		}
	}
}
