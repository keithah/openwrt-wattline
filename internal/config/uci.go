// Package config reads and writes /etc/config/wattline (UCI format).
package config

import (
	"fmt"
	"strings"
)

type UCISection struct {
	Type, Name string
	Options    map[string]string
	Lists      map[string][]string
}

type UCIDoc struct{ Sections []*UCISection }

func newSection(typ, name string) *UCISection {
	return &UCISection{Type: typ, Name: name,
		Options: map[string]string{}, Lists: map[string][]string{}}
}

// unquote strips one layer of single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

func ParseUCI(src string) (*UCIDoc, error) {
	doc := &UCIDoc{}
	var cur *UCISection
	for lineno, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) < 2 {
			return nil, fmt.Errorf("line %d: malformed %q", lineno+1, line)
		}
		rest := strings.TrimSpace(fields[1])
		switch fields[0] {
		case "config":
			parts := strings.SplitN(rest, " ", 2)
			name := ""
			if len(parts) == 2 {
				name = unquote(strings.TrimSpace(parts[1]))
			}
			cur = newSection(unquote(parts[0]), name)
			doc.Sections = append(doc.Sections, cur)
		case "option", "list":
			if cur == nil {
				return nil, fmt.Errorf("line %d: %s outside section", lineno+1, fields[0])
			}
			kv := strings.SplitN(rest, " ", 2)
			if len(kv) != 2 {
				return nil, fmt.Errorf("line %d: malformed %q", lineno+1, line)
			}
			key, val := unquote(kv[0]), unquote(strings.TrimSpace(kv[1]))
			if fields[0] == "option" {
				cur.Options[key] = val
			} else {
				cur.Lists[key] = append(cur.Lists[key], val)
			}
		default:
			return nil, fmt.Errorf("line %d: unknown keyword %q", lineno+1, fields[0])
		}
	}
	return doc, nil
}

func (d *UCIDoc) Serialize() string {
	var b strings.Builder
	for _, s := range d.Sections {
		fmt.Fprintf(&b, "config %s '%s'\n", s.Type, s.Name)
		for _, k := range sortedKeys(s.Options) {
			fmt.Fprintf(&b, "\toption %s '%s'\n", k, s.Options[k])
		}
		for _, k := range sortedKeys(s.Lists) {
			for _, v := range s.Lists[k] {
				fmt.Fprintf(&b, "\tlist %s '%s'\n", k, v)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func (d *UCIDoc) Find(typ, name string) *UCISection {
	for _, s := range d.Sections {
		if s.Type == typ && s.Name == name {
			return s
		}
	}
	return nil
}
