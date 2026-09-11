package cli

import "sync"

type argumentMeta struct {
	Name        string `json:"name"`
	Requirement string `json:"requirement"`
	Description string `json:"description"`
	Omitted     string `json:"omitted,omitempty"`
}

type commandMeta struct {
	Purpose   string          `json:"purpose"`
	Arguments []argumentMeta  `json:"arguments,omitempty"`
	Reads     []string        `json:"reads,omitempty"`
	Changes   []string        `json:"changes,omitempty"`
	Preserves []string        `json:"preserves,omitempty"`
	Output    []string        `json:"output,omitempty"`
	Examples  []string        `json:"examples,omitempty"`
	Notes     []string        `json:"notes,omitempty"`
	Context   helpContextKind `json:"-"`
}

type helpContextKind string

const (
	helpContextNone     helpContextKind = "none"
	helpContextGeneral  helpContextKind = "general"
	helpContextSelected helpContextKind = "selected"
)

var commandMetadata sync.Map // path string -> commandMeta

func attachCommandMeta(path string, meta commandMeta) {
	if meta.Purpose == "" {
		meta.Purpose = "Manage EVE."
	}
	commandMetadata.Store(path, meta)
}

func metadataForPath(path string) commandMeta {
	if value, ok := commandMetadata.Load(path); ok {
		return value.(commandMeta)
	}
	return commandMeta{Purpose: "Manage EVE.", Context: helpContextGeneral}
}

func qualifiedPath(parts ...string) string {
	path := "eve"
	for _, part := range parts {
		path += " " + part
	}
	return path
}
