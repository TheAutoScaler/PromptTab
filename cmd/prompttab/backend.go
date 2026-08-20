package main

import "context"

// CompletionRequest contains only context that is permitted to reach a remote
// backend. Keep local-only data out of this type by construction.
type CompletionRequest struct {
	WorkingDirectory string `json:"cwd,omitempty"`
	Prefix           string `json:"prefix,omitempty"`
	Suffix           string `json:"suffix,omitempty"`
	Prompt           string `json:"prompt,omitempty"`
}

// LocalOnlyContext must only be passed to localCompletionBackend.
type LocalOnlyContext struct {
	RecentCommands []string `json:"recent_commands,omitempty"`
}

type completionBackend interface {
	Generate(context.Context, CompletionRequest) (string, error)
}

type localCompletionBackend interface {
	GenerateLocal(context.Context, CompletionRequest, LocalOnlyContext) (string, error)
}
