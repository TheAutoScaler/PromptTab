package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLocalSettings(url string) settings {
	return settings{localURL: url, localMaxTokens: 32, localTemperature: 0, localTimeout: 100 * time.Millisecond}
}

func TestLlamaBackendSuccessfulInfillAndContext(t *testing.T) {
	var received llamaInfillRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/infill" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write([]byte(`{"content":" status --short\nignored"}`))
	}))
	defer server.Close()
	backend, err := newLlamaBackend(testLocalSettings(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	result, err := backend.GenerateLocal(context.Background(), CompletionRequest{WorkingDirectory: "/work/repo", Prefix: "git", Suffix: " | head"}, LocalOnlyContext{RecentCommands: []string{"git diff", "git status"}})
	if err != nil {
		t.Fatal(err)
	}
	if result != " status --short" {
		t.Fatalf("result = %q", result)
	}
	if received.InputPrefix != "git" || received.InputSuffix != " | head" {
		t.Fatalf("cursor context = %#v", received)
	}
	if received.NPredict != 32 || received.Temperature != 0 {
		t.Fatalf("generation config = %#v", received)
	}
	if len(received.InputExtra) != 1 || !strings.Contains(received.InputExtra[0].Text, "/work/repo") || !strings.Contains(received.InputExtra[0].Text, "git status") {
		t.Fatalf("local context = %#v", received.InputExtra)
	}
}

func TestLlamaBackendGuidedCommandUsesCompletionEndpoint(t *testing.T) {
	var received struct {
		Prompt      string  `json:"prompt"`
		NPredict    int     `json:"n_predict"`
		Temperature float64 `json:"temperature"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/completion" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write([]byte("{\"content\":\" `aws s3 ls`\"}"))
	}))
	defer server.Close()
	backend, _ := newLlamaBackend(testLocalSettings(server.URL))
	result, err := backend.GenerateCommandLocal(context.Background(), CompletionRequest{WorkingDirectory: "/work", Prefix: "aws s3", Prompt: "list all buckets"}, LocalOnlyContext{RecentCommands: []string{"aws sts get-caller-identity"}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "aws s3 ls" {
		t.Fatalf("result = %q", result)
	}
	for _, want := range []string{"aws s3", "list all buckets"} {
		if !strings.Contains(received.Prompt, want) {
			t.Errorf("prompt missing %q: %q", want, received.Prompt)
		}
	}
	for _, excluded := range []string{"/work", "aws sts get-caller-identity"} {
		if strings.Contains(received.Prompt, excluded) {
			t.Errorf("guided prompt unexpectedly contains %q: %q", excluded, received.Prompt)
		}
	}
	if received.NPredict != 32 || received.Temperature != 0 {
		t.Fatalf("generation config = %#v", received)
	}
}

func TestLlamaBackendErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"malformed", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) }, "invalid llama-server response"},
		{"http500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, "HTTP 500"},
		{"empty", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"content":"  "}`)) }, "empty completion"},
		{"timeout", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(`{"content":"late"}`))
		}, "request failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			config := testLocalSettings(server.URL)
			if test.name == "timeout" {
				config.localTimeout = 10 * time.Millisecond
			}
			backend, _ := newLlamaBackend(config)
			_, err := backend.GenerateLocal(context.Background(), CompletionRequest{Prefix: "g"}, LocalOnlyContext{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLlamaBackendUnavailable(t *testing.T) {
	backend, _ := newLlamaBackend(testLocalSettings("http://127.0.0.1:1"))
	_, err := backend.GenerateLocal(context.Background(), CompletionRequest{Prefix: "git"}, LocalOnlyContext{})
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestLlamaBackendHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			t.Errorf("path = %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	backend, _ := newLlamaBackend(testLocalSettings(server.URL))
	if !backend.Healthy(context.Background()) {
		t.Fatal("healthy llama-server reported unavailable")
	}
	server.Close()
	if backend.Healthy(context.Background()) {
		t.Fatal("stopped llama-server reported healthy")
	}
}
