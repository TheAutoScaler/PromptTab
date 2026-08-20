package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultBackendIsCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PROMPTTAB_HOME", home)
	t.Setenv("PROMPTTAB_BACKEND", "")
	config, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if config.backend != "codex" {
		t.Fatalf("backend = %q", config.backend)
	}
}

func TestSelectLocalBackendAndConfiguration(t *testing.T) {
	home := t.TempDir()
	contents := "backend = \"local\"\n\n[local]\nname = \"Test Coder\"\nurl = \"http://127.0.0.1:9999\"\nmax_tokens = 17\ntemperature = 0.25\ntimeout_ms = 321\n"
	if err := os.WriteFile(filepath.Join(home, "prompttab.toml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTTAB_HOME", home)
	config, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if config.backend != "local" || config.localURL != "http://127.0.0.1:9999" || config.localMaxTokens != 17 || config.localTemperature != .25 || config.localTimeout.Milliseconds() != 321 {
		t.Fatalf("config = %#v", config)
	}
	if config.localName != "Test Coder" {
		t.Fatalf("local name = %q", config.localName)
	}
	if filepath.Base(config.socketPath) != "local-server.sock" {
		t.Fatalf("local socket = %q", config.socketPath)
	}
}

func TestAutoBackendSelection(t *testing.T) {
	state := &serverState{config: settings{backend: "auto"}}
	if state.activeBackend() != "codex" {
		t.Fatalf("unhealthy auto backend = %q", state.activeBackend())
	}
	state.localHealthy.Store(true)
	if state.activeBackend() != "local" {
		t.Fatalf("healthy auto backend = %q", state.activeBackend())
	}
}

func TestCompletionAndCommandModesAreDistinct(t *testing.T) {
	if !validRequestMode("complete") || !validRequestMode("command") {
		t.Fatal("expected modes to be supported")
	}
	if _, err := modeInstructions("complete"); err == nil {
		t.Fatal("local completion mode must not have Codex instructions")
	}
}

func TestCodexPromptContainsOnlyRemoteSafeContext(t *testing.T) {
	remote := CompletionRequest{WorkingDirectory: "/safe/cwd", Prefix: "terraform state ", Suffix: " | head", Prompt: "complete this"}
	local := LocalOnlyContext{RecentCommands: []string{"SECRET_HISTORY_SENTINEL", "PREVIOUS_COMMAND_SENTINEL"}}
	prompt := codexPrompt(remote)
	for _, want := range []string{remote.WorkingDirectory, remote.Prefix, remote.Suffix, remote.Prompt} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Codex prompt missing %q", want)
		}
	}
	for _, command := range local.RecentCommands {
		if strings.Contains(prompt, command) {
			t.Fatalf("local-only history leaked into Codex prompt: %q", command)
		}
	}
}

func TestBrokerJSONDoesNotFlattenLocalContext(t *testing.T) {
	request := brokerRequest{Mode: "command", Completion: CompletionRequest{Prefix: "git"}, Local: LocalOnlyContext{RecentCommands: []string{"PRIVATE_HISTORY"}}}
	if strings.Contains(codexPrompt(request.Completion), "PRIVATE_HISTORY") {
		t.Fatal("local context reached remote request construction")
	}
}
