package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	unknownKeyRE = regexp.MustCompile(`^line (\d+): field (.*) not found in type (\S+)$`)
	wrongTypeRE  = regexp.MustCompile("^line (\\d+): cannot unmarshal !!\\w+(?: `(.*)`)? into (\\S+)$")

	// sections are the config's structs by Go type name, for the keys an
	// unknown key may be a typo of.
	sections = func() map[string]reflect.Type {
		m := map[string]reflect.Type{}
		for _, v := range []any{Config{}, Target{}, Endpoint{}, Alert{}, Notifier{}} {
			m[reflect.TypeOf(v).String()] = reflect.TypeOf(v)
		}
		return m
	}()

	// wants describe a Go type for a type mismatch.
	wants = map[string]string{
		"time.Duration": "a duration such as 30s or 5m",
		"uint64":        "a whole number, 0 or more",
		"int":           "a whole number",
		"bool":          "true or false",
		"string":        "a single value, not a list or mapping",
	}
)

// yamlError rewrites the decoder's type errors in the config's terms: an
// unknown key names its section and the closest known key, a wrong type
// says what is expected. No Go type names are shown. raw is the document
// that failed to decode; other errors are returned as they are.
func yamlError(raw []byte, err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return err
	}
	var root yaml.Node
	if yaml.Unmarshal(raw, &root) != nil {
		return err
	}
	var es []entry
	walk(&root, "", &es)
	msgs := make([]string, len(te.Errors))
	for i, e := range te.Errors {
		msgs[i] = rewrite(e, es)
	}
	return errors.New(strings.Join(msgs, "\n"))
}

// entry is a value in the document: a mapping key's value or a list item.
type entry struct {
	key, path        string // key is "" for list items
	keyLine, valLine int
	scalar           bool
	value            string
}

func walk(n *yaml.Node, path string, es *[]entry) {
	add := func(key, p string, keyLine int, v *yaml.Node) {
		*es = append(*es, entry{key, p, keyLine, v.Line, v.Kind == yaml.ScalarNode, v.Value})
		walk(v, p, es)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walk(c, path, es)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			p := k.Value
			if path != "" {
				p = path + "." + p
			}
			add(k.Value, p, k.Line, n.Content[i+1])
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			add("", fmt.Sprintf("%s[%d]", path, i), c.Line, c)
		}
	}
}

func rewrite(msg string, es []entry) string {
	if m := unknownKeyRE.FindStringSubmatch(msg); m != nil {
		line, _ := strconv.Atoi(m[1])
		out := fmt.Sprintf("line %d: unknown key %q", line, m[2])
		for _, e := range es {
			if e.key == m[2] && e.keyLine == line {
				if i := strings.LastIndex(e.path, "."); i >= 0 {
					out += " in " + e.path[:i]
				} else {
					out += " at the top level"
				}
				break
			}
		}
		if s := suggest(m[2], sections[m[3]]); s != "" {
			out += fmt.Sprintf(" (did you mean %q?)", s)
		}
		return out
	}
	if m := wrongTypeRE.FindStringSubmatch(msg); m != nil {
		line, _ := strconv.Atoi(m[1])
		want, ok := wants[m[3]]
		switch {
		case ok:
		case strings.HasPrefix(m[3], "[]"):
			want = "a list"
		default:
			want = "a mapping of keys and values"
		}
		// The decoder shows scalars, cut to 7 characters and "...", and no
		// value for lists and mappings: the innermost of those on the line.
		var found *entry
		for i, e := range es {
			short, cut := strings.CutSuffix(m[2], "...")
			if e.valLine == line && (e.scalar && (e.value == m[2] || cut && strings.HasPrefix(e.value, short)) || !e.scalar && m[2] == "") {
				found = &es[i]
				if e.scalar {
					break
				}
			}
		}
		switch {
		case found == nil:
			return fmt.Sprintf("line %d: must be %s", line, want)
		case found.scalar:
			return fmt.Sprintf("line %d: %s %q: must be %s", line, found.path, redactURL(found.value), want)
		}
		return fmt.Sprintf("line %d: %s: must be %s", line, found.path, want)
	}
	return msg
}

// suggest is the yaml key of t closest to key, if it is close enough to be
// a typo.
func suggest(key string, t reflect.Type) string {
	if t == nil {
		return ""
	}
	best, bestD := "", max(1, len(key)/3)+1
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		if d := distance(strings.ToLower(key), tag); d < bestD {
			best, bestD = tag, d
		}
	}
	return best
}

// distance is the Levenshtein distance of a and b.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := range ra {
		cur := make([]int, len(rb)+1)
		cur[0] = i + 1
		for j := range rb {
			cost := 1
			if ra[i] == rb[j] {
				cost = 0
			}
			cur[j+1] = min(prev[j+1]+1, cur[j]+1, prev[j]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
