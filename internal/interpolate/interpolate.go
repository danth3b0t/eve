// Package interpolate implements EVE's non-executable reference language.
package interpolate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxBytes = 2 << 20

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

type Error struct {
	Offset int // one-based byte offset in the expression
	Reason string
}

func (e *Error) Error() string {
	// Never include expression text: a literal may contain a developer secret.
	return fmt.Sprintf("E_REFERENCE at byte %d: %s", e.Offset, e.Reason)
}

type segment struct {
	text   string
	ref    bool
	offset int
}

type Expression struct{ segments []segment }

// Parse accepts only the public reference vocabulary. It does not read process
// environment, execute anything, or permit credential/env-to-env references.
func Parse(input string) (Expression, error) {
	if len(input) > MaxBytes || !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return Expression{}, &Error{1, "input too large or invalid text"}
	}
	var parts []segment
	var literal strings.Builder
	flush := func() {
		if literal.Len() != 0 {
			parts = append(parts, segment{text: literal.String()})
			literal.Reset()
		}
	}
	for i := 0; i < len(input); {
		switch {
		case strings.HasPrefix(input[i:], "$${"):
			literal.WriteString("${")
			i += 3
		case strings.HasPrefix(input[i:], "${"):
			end := strings.IndexByte(input[i+2:], '}')
			if end < 0 {
				return Expression{}, &Error{i + 1, "unterminated reference"}
			}
			end += i + 2
			ref := input[i+2 : end]
			if !allowed(ref) {
				return Expression{}, &Error{i + 1, "unsupported reference"}
			}
			flush()
			parts = append(parts, segment{text: ref, ref: true, offset: i + 1})
			i = end + 1
		default:
			literal.WriteByte(input[i])
			i++
		}
	}
	flush()
	return Expression{parts}, nil
}

func allowed(ref string) bool {
	p := strings.Split(ref, ".")
	if len(p) == 2 && p[0] == "workspace" {
		switch p[1] {
		case "id", "slug", "branch", "port_base":
			return true
		}
	}
	if len(p) < 3 || !identifier.MatchString(p[1]) {
		return false
	}
	if p[0] == "resources" && len(p) == 3 {
		switch p[2] {
		case "url", "site_url", "deployment", "name", "reference":
			return true
		}
	}
	if p[0] == "services" {
		if len(p) == 3 {
			return p[2] == "port" || p[2] == "url"
		}
		return len(p) == 5 && p[2] == "ports" && identifier.MatchString(p[3]) && p[3] != "primary" && p[4] == "port"
	}
	return false
}

func (e Expression) References() []string {
	var refs []string
	for _, part := range e.segments {
		if part.ref {
			refs = append(refs, part.text)
		}
	}
	return refs
}

// Evaluate returns no partial result on failure. Supplied values are not
// reparsed, so even a value containing ${...} cannot introduce another lookup.
func (e Expression) Evaluate(values map[string]string) (string, error) {
	var out strings.Builder
	for _, part := range e.segments {
		value := part.text
		if part.ref {
			var ok bool
			value, ok = values[part.text]
			if !ok {
				return "", &Error{part.offset, "reference has no value"}
			}
		}
		if len(value) > MaxBytes-out.Len() {
			return "", &Error{part.offset, "resolved value too large"}
		}
		out.WriteString(value)
	}
	return out.String(), nil
}
