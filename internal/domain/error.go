// Package domain contains shared, value-free diagnostics for side-effecting core code.
package domain

import "fmt"

// Error contains only safe metadata. Do not attach SQL, dotenv lines, credentials,
// subprocess output, or unredacted provider responses as messages or causes.
type Error struct {
	Code    string
	Message string
	Path    string
	Port    int
}

func (e *Error) Error() string {
	s := e.Code + ": " + e.Message
	if e.Path != "" {
		s += fmt.Sprintf(" path=%q", e.Path)
	}
	if e.Port != 0 {
		s += fmt.Sprintf(" port=%d", e.Port)
	}
	return s
}
