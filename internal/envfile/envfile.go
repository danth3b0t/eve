// Package envfile prepares lossless native dotenv images. It performs no I/O
// and never evaluates expansion or command substitution.
package envfile

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxBytes = 2 << 20

type Error struct {
	Code   string
	Key    string
	Lines  []int
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s key=%q lines=%v: %s", e.Code, e.Key, e.Lines, e.Reason)
}

func ValidKey(key string) bool {
	if key == "" || !(letter(key[0]) || key[0] == '_') {
		return false
	}
	for i := 1; i < len(key); i++ {
		if !(letter(key[i]) || key[i] == '_' || digit(key[i])) {
			return false
		}
	}
	return true
}

// ValidateValue constrains generated values, not existing unmanaged values.
func ValidateValue(value string) error {
	if len(value) > MaxBytes {
		return &Error{Code: "E_ENV_SIZE", Reason: "value exceeds 2 MiB"}
	}
	for _, r := range value {
		if r < 33 || r > 126 || strings.ContainsRune("\"'`\\$#", r) {
			return &Error{Code: "E_ENV_SERIALIZATION", Reason: "generated value is outside the portable dotenv subset"}
		}
	}
	return nil
}

type assignment struct{ start, end, line int }
type Document struct {
	input []byte
	keys  map[string][]assignment
	eol   string
}

// GeneratedValue reads exactly one portable, unquoted EVE-generated value.
// It is not a general dotenv evaluator; richer user-authored values are refused.
func (d *Document) GeneratedValue(key string) (string, error) {
	entries := d.keys[key]
	if !ValidKey(key) || len(entries) != 1 {
		return "", &Error{Code: "E_ENV_VALUE", Key: key, Reason: "exactly one generated assignment is required"}
	}
	a := entries[0]
	value := string(d.input[a.start:a.end])
	if err := ValidateValue(value); err != nil {
		return "", err
	}
	return value, nil
}

func letter(b byte) bool       { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
func digit(b byte) bool        { return b >= '0' && b <= '9' }
func space(b byte) bool        { return b == ' ' || b == '\t' }
func quote(b byte) bool        { return b == '\'' || b == '"' || b == '`' }
func inputKeyByte(b byte) bool { return letter(b) || digit(b) || b == '_' || b == '-' || b == '.' }
func syntax(line int) error {
	return &Error{Code: "E_ENV_SYNTAX", Lines: []int{line}, Reason: "ambiguous assignment boundary or malformed quoting"}
}

// lineEnd excludes a CRLF's CR. next includes its newline, if present.
func lineEnd(input []byte, from int) (end, next int) {
	n := bytes.IndexByte(input[from:], '\n')
	if n < 0 {
		return len(input), len(input)
	}
	end = from + n
	next = end + 1
	if end > 0 && input[end-1] == '\r' {
		end--
	}
	return end, next
}

func Parse(input []byte) (*Document, error) {
	if len(input) > MaxBytes {
		return nil, &Error{Code: "E_ENV_SIZE", Reason: "input exceeds 2 MiB"}
	}
	if !utf8.Valid(input) || bytes.IndexByte(input, 0) >= 0 {
		return nil, &Error{Code: "E_ENV_SYNTAX", Reason: "invalid UTF-8 or NUL byte"}
	}
	// Bare CR has inconsistent assignment-boundary semantics across loaders.
	for i, b := range input {
		if b == '\r' && (i+1 == len(input) || input[i+1] != '\n') {
			return nil, &Error{Code: "E_ENV_SYNTAX", Reason: "bare carriage return is unsupported"}
		}
	}
	d := &Document{input: bytes.Clone(input), keys: make(map[string][]assignment), eol: "\n"}
	if n := bytes.IndexByte(input, '\n'); n > 0 && input[n-1] == '\r' {
		d.eol = "\r\n"
	}
	i, line := 0, 1
	if bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) {
		i = 3
	}
	for i < len(input) {
		end, next := lineEnd(input, i)
		p := i
		for p < end && space(input[p]) {
			p++
		}
		if p == end || input[p] == '#' {
			i, line = next, line+1
			continue
		}
		if bytes.HasPrefix(input[p:end], []byte("export")) && p+6 < end && space(input[p+6]) {
			p += 7
			for p < end && space(input[p]) {
				p++
			}
		}
		keyStart := p
		for p < end && inputKeyByte(input[p]) {
			p++
		}
		keyEnd := p
		for p < end && space(input[p]) {
			p++
		}
		if keyStart == keyEnd || p == end || input[p] != '=' {
			// Retain uninterpreted single-line text only when it cannot hide
			// a quoted assignment. Reject dotenv's alternate colon syntax:
			// treating it as absent could append a duplicate active key.
			if bytes.ContainsAny(input[i:end], "\"'`=:") {
				return nil, syntax(line)
			}
			i, line = next, line+1
			continue
		}
		key := string(input[keyStart:keyEnd])
		p++
		rawStart := p
		for p < end && space(input[p]) {
			p++
		}
		a := assignment{start: p, end: p, line: line}
		switch {
		case p == end || input[p] == '#':
			// Insert before existing padding on an empty RHS, preserving the
			// whitespace separating a new value from its inline comment.
			a.start, a.end = rawStart, rawStart
		case quote(input[p]):
			q := input[p]
			p++
			for p < len(input) && input[p] != q {
				if input[p] == '\\' && p+1 < len(input) {
					if input[p+1] == '\n' {
						line++
					}
					p += 2
				} else {
					if input[p] == '\n' {
						line++
					}
					p++
				}
			}
			if p == len(input) {
				return nil, syntax(a.line)
			}
			p++
			a.end = p // the value span includes its quote delimiters
			end, next = lineEnd(input, p)
			for p < end && space(input[p]) {
				p++
			}
			if p < end && input[p] != '#' {
				return nil, syntax(a.line)
			}
		default:
			for p < end && input[p] != '#' {
				if quote(input[p]) {
					return nil, syntax(a.line)
				}
				if input[p] == '\\' && (p+1 == end || space(input[p+1]) || input[p+1] == '#' || quote(input[p+1])) {
					return nil, syntax(a.line)
				}
				p++
			}
			a.end = p
			for a.end > a.start && space(input[a.end-1]) {
				a.end--
			}
		}
		d.keys[key] = append(d.keys[key], a)
		i, line = next, line+1
	}
	return d, nil
}

// Apply prepares the complete image or returns nil. Only owned value spans
// change; comments, padding, other assignments and line endings remain intact.
func (d *Document) Apply(values map[string]string) ([]byte, error) {
	type edit struct {
		assignment
		value string
	}
	var edits []edit
	var missing []string
	size := len(d.input)
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if !ValidKey(key) {
			return nil, &Error{Code: "E_ENV_KEY", Reason: "invalid managed environment key"}
		}
		if err := ValidateValue(values[key]); err != nil {
			e := *err.(*Error)
			e.Key = key
			return nil, &e
		}
		found := d.keys[key]
		if len(found) > 1 {
			lines := make([]int, len(found))
			for i, a := range found {
				lines[i] = a.line
			}
			return nil, &Error{Code: "E_ENV_DUPLICATE", Key: key, Lines: lines, Reason: "managed key has multiple definitions"}
		}
		if len(found) == 0 {
			missing = append(missing, key)
			size += len(key) + 1 + len(values[key]) + len(d.eol)
		} else {
			a := found[0]
			edits = append(edits, edit{a, values[key]})
			size += len(values[key]) - (a.end - a.start)
		}
	}
	separator := len(missing) > 0 && len(d.input) > 0 && d.input[len(d.input)-1] != '\n'
	if separator {
		size += len(d.eol)
	}
	if size > MaxBytes {
		return nil, &Error{Code: "E_ENV_SIZE", Reason: "output exceeds 2 MiB"}
	}
	slices.SortFunc(edits, func(a, b edit) int { return a.start - b.start })
	out := make([]byte, 0, size)
	last := 0
	for _, e := range edits {
		out = append(out, d.input[last:e.start]...)
		out = append(out, e.value...)
		last = e.end
	}
	out = append(out, d.input[last:]...)
	if separator {
		out = append(out, d.eol...)
	}
	for _, key := range missing {
		out = append(out, key...)
		out = append(out, '=')
		out = append(out, values[key]...)
		out = append(out, d.eol...)
	}
	return out, nil
}
