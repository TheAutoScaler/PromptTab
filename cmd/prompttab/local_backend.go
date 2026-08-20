package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type llamaBackend struct {
	endpoint           string
	completionEndpoint string
	healthURL          string
	maxTokens          int
	temperature        float64
	client             *http.Client
}

type llamaInfillRequest struct {
	InputPrefix string            `json:"input_prefix"`
	InputSuffix string            `json:"input_suffix"`
	InputExtra  []llamaInputExtra `json:"input_extra,omitempty"`
	NPredict    int               `json:"n_predict"`
	Temperature float64           `json:"temperature"`
	Stream      bool              `json:"stream"`
	CachePrompt bool              `json:"cache_prompt"`
	Stop        []string          `json:"stop"`
}

type llamaInputExtra struct {
	Filename string `json:"filename"`
	Text     string `json:"text"`
}

func newLlamaBackend(config settings) (*llamaBackend, error) {
	parsed, err := url.Parse(config.localURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid local backend URL %q", config.localURL)
	}
	return &llamaBackend{
		endpoint:           strings.TrimRight(config.localURL, "/") + "/infill",
		completionEndpoint: strings.TrimRight(config.localURL, "/") + "/completion",
		healthURL:          strings.TrimRight(config.localURL, "/") + "/health",
		maxTokens:          config.localMaxTokens,
		temperature:        config.localTemperature,
		client:             &http.Client{Timeout: config.localTimeout},
	}, nil
}

func (backend *llamaBackend) Healthy(ctx context.Context) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, backend.healthURL, nil)
	if err != nil {
		return false
	}
	response, err := backend.client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func (backend *llamaBackend) GenerateLocal(ctx context.Context, request CompletionRequest, local LocalOnlyContext) (string, error) {
	if len(local.RecentCommands) > 8 {
		local.RecentCommands = local.RecentCommands[len(local.RecentCommands)-8:]
	}
	extra := []llamaInputExtra{{Filename: "prompttab-context.txt", Text: localContextText(request.WorkingDirectory, local.RecentCommands)}}
	payload := llamaInfillRequest{
		InputPrefix: request.Prefix, InputSuffix: request.Suffix, InputExtra: extra,
		NPredict: backend.maxTokens, Temperature: backend.temperature, Stream: false,
		CachePrompt: true, Stop: []string{"\n", "\r"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, backend.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := backend.client.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("local llama-server request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxMessageSize+1))
	if err != nil {
		return "", err
	}
	if len(responseBody) > maxMessageSize {
		return "", errors.New("llama-server response exceeds 1 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("llama-server returned HTTP %d", response.StatusCode)
	}
	var decoded struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return "", fmt.Errorf("invalid llama-server response: %w", err)
	}
	completion := sanitizeCommandCompletion(decoded.Content)
	if strings.TrimSpace(completion) == "" {
		return "", errors.New("llama-server returned an empty completion")
	}
	return completion, nil
}

func (backend *llamaBackend) GenerateCommandLocal(ctx context.Context, request CompletionRequest, _ LocalOnlyContext) (string, error) {
	prompt := guidedCommandPrompt(request)
	payload := map[string]any{"prompt": prompt, "n_predict": backend.maxTokens, "temperature": backend.temperature, "stream": false, "cache_prompt": true, "stop": []string{"\n", "\r"}}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, backend.completionEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := backend.client.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("local llama-server request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxMessageSize+1))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("llama-server returned HTTP %d", response.StatusCode)
	}
	var decoded struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return "", fmt.Errorf("invalid llama-server response: %w", err)
	}
	command := sanitizeGeneratedCommand(decoded.Content)
	if command == "" {
		return "", errors.New("llama-server returned an empty command")
	}
	return command, nil
}

func guidedCommandPrompt(request CompletionRequest) string {
	parts := []string{
		"You generate one macOS Bash command. Return only the command, no Markdown.",
		"Requirement: " + compactPromptValue(request.Prompt, 512) + ".",
	}
	parts = append(parts, "Current command: "+compactPromptValue(request.Prefix+request.Suffix, 1024)+".", "Command:")
	return strings.Join(parts, " ")
}

func sanitizeGeneratedCommand(value string) string {
	value = strings.TrimSpace(sanitizeCommandCompletion(value))
	if strings.HasPrefix(value, "```") && strings.HasSuffix(value, "```") {
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "```"), "```"))
	}
	if len(value) >= 2 && value[0] == '`' && value[len(value)-1] == '`' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func compactPromptValue(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func localContextText(cwd string, commands []string) string {
	var builder strings.Builder
	if cwd != "" {
		fmt.Fprintf(&builder, "Working directory: %s\n", cwd)
	}
	if len(commands) > 0 {
		builder.WriteString("Recent shell commands (local context only):\n")
		for _, command := range commands {
			command = strings.TrimSpace(command)
			if command != "" {
				fmt.Fprintf(&builder, "- %s\n", command)
			}
		}
	}
	result := builder.String()
	if len(result) > 4096 {
		result = result[len(result)-4096:]
	}
	return result
}

func sanitizeCommandCompletion(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		value = value[:index]
	}
	// Leading whitespace is often the semantically essential separator between
	// the existing prefix and its completion.
	return strings.TrimRight(value, " \t")
}
