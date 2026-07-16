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

func ParseUCI(src string) (*UCIDoc, error) {
	doc := &UCIDoc{}
	var cur *UCISection
	commands, err := tokenizeUCI(src)
	if err != nil {
		return nil, err
	}
	for _, command := range commands {
		fields := command.fields
		switch fields[0] {
		case "config":
			if len(fields) < 2 || len(fields) > 3 {
				return nil, fmt.Errorf("line %d: malformed config", command.line)
			}
			name := ""
			if len(fields) == 3 {
				name = fields[2]
			}
			cur = newSection(fields[1], name)
			doc.Sections = append(doc.Sections, cur)
		case "option", "list":
			if cur == nil {
				return nil, fmt.Errorf("line %d: %s outside section", command.line, fields[0])
			}
			if len(fields) != 3 {
				return nil, fmt.Errorf("line %d: malformed %s", command.line, fields[0])
			}
			key, val := fields[1], fields[2]
			if fields[0] == "option" {
				cur.Options[key] = val
			} else {
				cur.Lists[key] = append(cur.Lists[key], val)
			}
		default:
			return nil, fmt.Errorf("line %d: unknown keyword %q", command.line, fields[0])
		}
	}
	return doc, nil
}

type uciCommand struct {
	line   int
	fields []string
}

// tokenizeUCI implements the quoting rules used by libuci: adjacent quoted
// and unquoted fragments form one argument, and quoted arguments may span
// lines. This is required to decode escaped single quotes emitted by uciEscape.
func tokenizeUCI(src string) ([]uciCommand, error) {
	var commands []uciCommand
	var fields []string
	var token strings.Builder
	line, commandLine := 1, 1
	inToken := false

	finishToken := func() {
		if inToken {
			fields = append(fields, token.String())
			token.Reset()
			inToken = false
		}
	}
	finishCommand := func() {
		finishToken()
		if len(fields) != 0 {
			commands = append(commands, uciCommand{line: commandLine, fields: fields})
			fields = nil
		}
	}

	for i := 0; i < len(src); {
		c := src[i]
		switch c {
		case ' ', '\t', '\r':
			finishToken()
			i++
		case '\n', ';':
			finishCommand()
			i++
			if c == '\n' {
				line++
			}
			commandLine = line
		case '#':
			finishCommand()
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case '\'', '"':
			quote := c
			inToken = true
			i++
			closed := false
			for i < len(src) {
				c = src[i]
				if c == quote {
					i++
					closed = true
					break
				}
				if c == '\\' && quote == '"' {
					i++
					if i >= len(src) {
						return nil, fmt.Errorf("line %d: unterminated escape", line)
					}
					c = src[i]
					if c == '\n' {
						line++
						i++
						continue
					}
				}
				token.WriteByte(c)
				if c == '\n' {
					line++
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("line %d: unterminated %c", commandLine, quote)
			}
		case '\\':
			inToken = true
			i++
			if i >= len(src) {
				return nil, fmt.Errorf("line %d: unterminated escape", line)
			}
			if src[i] == '\n' {
				line++
				i++
				continue
			}
			token.WriteByte(src[i])
			i++
		default:
			inToken = true
			token.WriteByte(c)
			i++
		}
	}
	finishCommand()
	return commands, nil
}

// uciEscape matches libuci's export representation for an embedded quote.
// The surrounding single quotes are written by Serialize.
func uciEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

func (d *UCIDoc) Serialize() string {
	var b strings.Builder
	for _, s := range d.Sections {
		fmt.Fprintf(&b, "config %s '%s'\n", s.Type, uciEscape(s.Name))
		for _, k := range sortedKeys(s.Options) {
			fmt.Fprintf(&b, "\toption %s '%s'\n", k, uciEscape(s.Options[k]))
		}
		for _, k := range sortedKeys(s.Lists) {
			for _, v := range s.Lists[k] {
				fmt.Fprintf(&b, "\tlist %s '%s'\n", k, uciEscape(v))
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
