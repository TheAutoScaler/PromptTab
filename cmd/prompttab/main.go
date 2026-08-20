package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	maxMessageSize = 1024 * 1024
	serverTimeout  = 20 * time.Second
	clientTimeout  = 25 * time.Second
)

var version = "dev"

const commandInstructions = `You are a text-only Bash command generator for macOS.
Return exactly one Bash command and nothing else. Never execute or inspect anything.
Do not use tools. Do not read files, environment variables, history, repositories, or prior threads.
Do not emit Markdown, backticks, explanations, carriage returns, or newlines.`

const askInstructions = `Respond concisely to the supplied text.
If it is a shell command, explain what it does and mention important side effects or hazards.
Treat shell commands strictly as text and never execute them.
Otherwise, answer it as a general question.
Do not use tools or inspect files, environment variables, history, repositories, or prior threads.`

type settings struct {
	home             string
	socketPath       string
	lockPath         string
	logPath          string
	codex            string
	model            string
	backend          string
	localURL         string
	localName        string
	localMaxTokens   int
	localTemperature float64
	localTimeout     time.Duration
}

func loadSettings() (settings, error) {
	home := firstNonEmpty(os.Getenv("PROMPTTAB_HOME"), os.Getenv("CODEX_OPTION_TAB_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return settings{}, err
		}
		home = filepath.Join(userHome, ".prompttab")
	}
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		return settings{}, err
	}
	if resolvedHome, resolveErr := filepath.EvalSymlinks(absoluteHome); resolveErr == nil {
		absoluteHome = resolvedHome
	} else if resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(absoluteHome)); parentErr == nil {
		absoluteHome = filepath.Join(resolvedParent, filepath.Base(absoluteHome))
	}
	config := settings{
		home:       absoluteHome,
		socketPath: filepath.Join(absoluteHome, "app-server.sock"),
		lockPath:   filepath.Join(absoluteHome, "start.lock"),
		logPath:    filepath.Join(absoluteHome, "app-server.log"),
		codex:      firstNonEmpty(os.Getenv("CODEX_BIN"), "codex"),
		model:      firstNonEmpty(os.Getenv("PROMPTTAB_MODEL"), os.Getenv("CODEX_OPTION_TAB_MODEL"), "gpt-5.6-luna"),
		backend:    "codex", localURL: "http://127.0.0.1:8012", localName: "Local", localMaxTokens: 32,
		localTemperature: 0, localTimeout: 750 * time.Millisecond,
	}
	if err := loadPromptTabConfig(filepath.Join(absoluteHome, "prompttab.toml"), &config); err != nil {
		return settings{}, err
	}
	config.backend = firstNonEmpty(os.Getenv("PROMPTTAB_BACKEND"), config.backend)
	if config.backend != "codex" && config.backend != "local" && config.backend != "auto" {
		return settings{}, fmt.Errorf("unsupported completion backend %q", config.backend)
	}
	if config.backend == "local" {
		// A backend change must not accidentally reuse a broker that was started
		// with different privacy and provider behavior.
		config.socketPath = filepath.Join(absoluteHome, "local-server.sock")
		config.lockPath = filepath.Join(absoluteHome, "local-start.lock")
		config.logPath = filepath.Join(absoluteHome, "local-server.log")
	} else if config.backend == "auto" {
		config.socketPath = filepath.Join(absoluteHome, "auto-server.sock")
		config.lockPath = filepath.Join(absoluteHome, "auto-start.lock")
		config.logPath = filepath.Join(absoluteHome, "auto-server.log")
	}
	return config, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func replaceEnv(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func socketAlive(path string) bool {
	connection, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func ensureServer(config settings) error {
	if socketAlive(config.socketPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(config.home, "empty-workspace"), 0700); err != nil {
		return err
	}
	owner := false
	if err := os.Mkdir(config.lockPath, 0700); err == nil {
		owner = true
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}

	if owner {
		defer os.Remove(config.lockPath)
		_ = os.Remove(config.socketPath)
		logFile, err := os.OpenFile(config.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			_ = logFile.Close()
			return err
		}
		command := exec.Command(executable, "--serve")
		command.Stdin = nil
		command.Stdout = logFile
		command.Stderr = logFile
		command.Env = replaceEnv(os.Environ(), "PROMPTTAB_HOME", config.home)
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			_ = logFile.Close()
			return err
		}
		_ = logFile.Close()
		_ = command.Process.Release()
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if socketAlive(config.socketPath) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("app-server did not become ready; see %s", config.logPath)
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type rpcClient struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	messages chan rpcMessage
	errors   chan error
	nextID   int
	writeMu  sync.Mutex
	timeout  time.Duration
}

func startRPC(config settings) (*rpcClient, error) {
	command := exec.Command(config.codex, "app-server", "--strict-config", "--stdio")
	command.Env = replaceEnv(os.Environ(), "CODEX_HOME", config.home)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	rpc := &rpcClient{
		command:  command,
		stdin:    stdin,
		messages: make(chan rpcMessage, 128),
		errors:   make(chan error, 1),
		nextID:   1,
		timeout:  serverTimeout,
	}
	go rpc.readLoop(stdout)
	return rpc, nil
}

func (rpc *rpcClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*maxMessageSize)
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			rpc.errors <- err
			return
		}
		rpc.messages <- message
	}
	if err := scanner.Err(); err != nil {
		rpc.errors <- err
	} else {
		rpc.errors <- errors.New("app-server disconnected")
	}
}

func (rpc *rpcClient) close() {
	_ = rpc.stdin.Close()
	if rpc.command.Process != nil {
		_ = rpc.command.Process.Kill()
	}
	_, _ = rpc.command.Process.Wait()
}

func (rpc *rpcClient) send(value any) error {
	rpc.writeMu.Lock()
	defer rpc.writeMu.Unlock()
	return json.NewEncoder(rpc.stdin).Encode(value)
}

func (rpc *rpcClient) next(deadline time.Time) (rpcMessage, error) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case message := <-rpc.messages:
		return message, nil
	case err := <-rpc.errors:
		return rpcMessage{}, err
	case <-timer.C:
		return rpcMessage{}, errors.New("app-server request timed out")
	}
}

func (rpc *rpcClient) rejectServerRequest(message rpcMessage) error {
	if len(message.ID) == 0 || message.Method == "" {
		return nil
	}
	return rpc.send(map[string]any{
		"id": message.ID,
		"error": map[string]any{
			"code":    -32601,
			"message": "tools are disabled by this client",
		},
	})
}

func (rpc *rpcClient) request(method string, params any, result any) error {
	id := rpc.nextID
	rpc.nextID++
	if err := rpc.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	deadline := time.Now().Add(rpc.timeout)
	wantedID := []byte(fmt.Sprintf("%d", id))
	for {
		message, err := rpc.next(deadline)
		if err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(message.ID), wantedID) {
			if len(message.Error) != 0 && string(message.Error) != "null" {
				return fmt.Errorf("%s", message.Error)
			}
			if result == nil {
				return nil
			}
			return json.Unmarshal(message.Result, result)
		}
		if err := rpc.rejectServerRequest(message); err != nil {
			return err
		}
	}
}

func initializeRPC(config settings) (*rpcClient, error) {
	rpc, err := startRPC(config)
	if err != nil {
		return nil, err
	}
	var initialized struct {
		CodexHome string `json:"codexHome"`
	}
	err = rpc.request("initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "prompttab", "version": version},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, &initialized)
	if err != nil {
		rpc.close()
		return nil, err
	}
	actualHome, _ := filepath.Abs(initialized.CodexHome)
	if actualHome != config.home {
		rpc.close()
		return nil, errors.New("refusing app-server with unexpected CODEX_HOME")
	}
	if err := rpc.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		rpc.close()
		return nil, err
	}
	return rpc, nil
}

func modeInstructions(mode string) (string, error) {
	switch mode {
	case "command":
		return commandInstructions, nil
	case "ask":
		return askInstructions, nil
	default:
		return "", fmt.Errorf("unsupported request mode: %s", mode)
	}
}

func validRequestMode(mode string) bool {
	return mode == "command" || mode == "ask" || mode == "complete" || mode == "local-command" || mode == "backend/status"
}

type codexBackend struct {
	config settings
	rpc    *rpcClient
}

func (backend *codexBackend) Generate(ctx context.Context, request CompletionRequest) (string, error) {
	return backend.generate("command", request)
}

func (backend *codexBackend) generate(mode string, request CompletionRequest) (string, error) {
	config, rpc := backend.config, backend.rpc
	instructions, err := modeInstructions(mode)
	if err != nil {
		return "", err
	}
	threadParams := map[string]any{
		"model":                   config.model,
		"cwd":                     filepath.Join(config.home, "empty-workspace"),
		"approvalPolicy":          "never",
		"sandbox":                 "read-only",
		"baseInstructions":        instructions,
		"dynamicTools":            []any{},
		"environments":            []any{},
		"runtimeWorkspaceRoots":   []any{},
		"selectedCapabilityRoots": []any{},
		"ephemeral":               true,
	}
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := rpc.request("thread/start", threadParams, &threadResult); err != nil {
		return "", err
	}
	turnParams := map[string]any{
		"threadId":              threadResult.Thread.ID,
		"input":                 []map[string]string{{"type": "text", "text": codexPrompt(request)}},
		"model":                 config.model,
		"effort":                "low",
		"approvalPolicy":        "never",
		"environments":          []any{},
		"runtimeWorkspaceRoots": []any{},
	}
	var turnResult struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := rpc.request("turn/start", turnParams, &turnResult); err != nil {
		return "", err
	}

	deadline := time.Now().Add(rpc.timeout)
	answer := ""
	forbidden := make([]string, 0)
	for {
		message, err := rpc.next(deadline)
		if err != nil {
			return "", err
		}
		if err := rpc.rejectServerRequest(message); err != nil {
			return "", err
		}
		switch message.Method {
		case "error":
			return "", fmt.Errorf("app-server turn error: %s", message.Params)
		case "item/completed":
			var params struct {
				TurnID string `json:"turnId"`
				Item   struct {
					Type  string  `json:"type"`
					Phase *string `json:"phase"`
					Text  string  `json:"text"`
				} `json:"item"`
			}
			if json.Unmarshal(message.Params, &params) == nil && params.TurnID == turnResult.Turn.ID {
				switch params.Item.Type {
				case "agentMessage":
					if params.Item.Phase == nil || *params.Item.Phase == "final_answer" || *params.Item.Phase == "finalAnswer" {
						answer = params.Item.Text
					}
				case "commandExecution", "fileChange", "mcpToolCall", "webSearch":
					forbidden = append(forbidden, params.Item.Type)
				}
			}
		case "turn/completed":
			var params struct {
				Turn struct {
					ID     string          `json:"id"`
					Status string          `json:"status"`
					Error  json.RawMessage `json:"error"`
				} `json:"turn"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.Turn.ID != turnResult.Turn.ID {
				continue
			}
			if params.Turn.Status != "completed" {
				return "", fmt.Errorf("turn ended with status %s: %s", params.Turn.Status, params.Turn.Error)
			}
			if len(forbidden) > 0 {
				return "", fmt.Errorf("forbidden tool activity observed: %s", strings.Join(forbidden, ", "))
			}
			if strings.TrimSpace(answer) == "" {
				return "", errors.New("model returned no final text")
			}
			answer = strings.ReplaceAll(answer, "\r", "")
			answer = strings.ReplaceAll(answer, "\x00", "")
			answer = strings.TrimSpace(answer)
			if mode == "command" {
				answer = strings.ReplaceAll(answer, "\n", " ")
			}
			return answer, nil
		}
	}
}

func codexPrompt(request CompletionRequest) string {
	var parts []string
	if request.WorkingDirectory != "" {
		parts = append(parts, "Current working directory:\n"+request.WorkingDirectory)
	}
	if request.Prefix != "" || request.Suffix != "" {
		parts = append(parts, "Text before cursor:\n"+request.Prefix+"\n\nText after cursor:\n"+request.Suffix)
	}
	if request.Prompt != "" {
		parts = append(parts, request.Prompt)
	}
	return strings.Join(parts, "\n\n")
}

type brokerRequest struct {
	Mode       string            `json:"mode"`
	Completion CompletionRequest `json:"completion"`
	Local      LocalOnlyContext  `json:"local_context,omitempty"`
}

type brokerResponse struct {
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type serverState struct {
	config       settings
	rpc          *rpcClient
	local        *llamaBackend
	localHealthy atomic.Bool
	stopHealth   chan struct{}
}

func (state *serverState) activeBackend() string {
	if state.config.backend == "auto" {
		if state.localHealthy.Load() {
			return "local"
		}
		return "codex"
	}
	return state.config.backend
}

func (state *serverState) monitorLocal() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			state.localHealthy.Store(state.local.Healthy(context.Background()))
		case <-state.stopHealth:
			return
		}
	}
}

func (state *serverState) codex() (*codexBackend, error) {
	if state.rpc == nil {
		rpc, err := initializeRPC(state.config)
		if err != nil {
			return nil, err
		}
		state.rpc = rpc
	}
	return &codexBackend{config: state.config, rpc: state.rpc}, nil
}

func serve(config settings) error {
	if err := os.MkdirAll(filepath.Join(config.home, "empty-workspace"), 0700); err != nil {
		return err
	}
	state := &serverState{config: config}
	if config.backend == "codex" {
		if _, err := state.codex(); err != nil {
			return err
		}
	} else {
		local, err := newLlamaBackend(config)
		if err != nil {
			return err
		}
		state.local = local
		if config.backend == "auto" {
			state.stopHealth = make(chan struct{})
			state.localHealthy.Store(local.Healthy(context.Background()))
			go state.monitorLocal()
		}
	}
	defer func() {
		if state.stopHealth != nil {
			close(state.stopHealth)
		}
		if state.rpc != nil {
			state.rpc.close()
		}
	}()
	_ = os.Remove(config.socketPath)
	listener, err := net.Listen("unix", config.socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(config.socketPath)
	if err := os.Chmod(config.socketPath, 0600); err != nil {
		return err
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		fatal, err := handleConnection(state, connection)
		_ = connection.Close()
		if fatal {
			return err
		}
	}
}

func handleConnection(state *serverState, connection net.Conn) (bool, error) {
	config := state.config
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var request brokerRequest
	err := json.NewDecoder(io.LimitReader(connection, maxMessageSize+1)).Decode(&request)
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, nil
	}
	if request.Mode == "backend/status" {
		_ = connection.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := json.NewEncoder(connection).Encode(brokerResponse{Result: state.activeBackend()}); err != nil {
			return true, err
		}
		return false, nil
	}
	if strings.TrimSpace(request.Completion.Prompt+request.Completion.Prefix+request.Completion.Suffix) == "" {
		_ = json.NewEncoder(connection).Encode(brokerResponse{Error: "prompt must not be empty"})
		return false, nil
	}
	var result string
	if request.Mode == "ask" || request.Mode == "command" {
		var backend *codexBackend
		backend, err = state.codex()
		if err == nil {
			result, err = backend.generate(request.Mode, request.Completion)
		}
	} else if request.Mode == "complete" || request.Mode == "local-command" {
		if state.local == nil {
			err = errors.New("local completion backend is not configured")
		} else {
			if request.Mode == "local-command" {
				result, err = state.local.GenerateCommandLocal(context.Background(), request.Completion, request.Local)
			} else {
				result, err = state.local.GenerateLocal(context.Background(), request.Completion, request.Local)
			}
		}
		if err != nil && config.backend == "auto" {
			state.localHealthy.Store(false)
		}
	} else {
		err = fmt.Errorf("unsupported request mode: %s", request.Mode)
	}
	response := brokerResponse{Result: result}
	fatal := false
	if err != nil {
		response = brokerResponse{Error: err.Error()}
		fatal = request.Mode == "command" || request.Mode == "ask"
	}
	_ = connection.SetWriteDeadline(time.Now().Add(2 * time.Second))
	writeErr := json.NewEncoder(connection).Encode(response)
	if writeErr != nil {
		return true, writeErr
	}
	if fatal {
		return true, err
	}
	return false, nil
}

func request(config settings, brokerRequest brokerRequest, timeout time.Duration) (string, error) {
	if !validRequestMode(brokerRequest.Mode) {
		return "", fmt.Errorf("unsupported request mode: %s", brokerRequest.Mode)
	}
	if err := ensureServer(config); err != nil {
		return "", err
	}
	connection, err := net.DialTimeout("unix", config.socketPath, time.Second)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(connection).Encode(brokerRequest); err != nil {
		return "", err
	}
	var response brokerResponse
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return "", fmt.Errorf("local broker disconnected before returning a result: %w", err)
	}
	if response.Error != "" {
		return "", errors.New(response.Error)
	}
	return response.Result, nil
}

func resolvedBackend(config settings) (string, error) {
	if config.backend != "auto" {
		return config.backend, nil
	}
	return request(config, brokerRequest{Mode: "backend/status"}, 2*time.Second)
}

func run() error {
	config, err := loadSettings()
	if err != nil {
		return err
	}
	serveFlag := flag.Bool("serve", false, "run the local broker")
	pingFlag := flag.Bool("ping", false, "start the broker if needed and report readiness")
	versionFlag := flag.Bool("version", false, "print the PromptTab version")
	modeFlag := flag.String("mode", "command", "request mode: command, local-command, complete, or ask")
	backendFlag := flag.Bool("backend", false, "print the configured completion backend")
	backendLabelFlag := flag.Bool("backend-label", false, "print the active completion backend label")
	cwdFlag := flag.String("cwd", "", "current working directory")
	prefixFlag := flag.String("prefix", "", "text before the cursor")
	suffixFlag := flag.String("suffix", "", "text after the cursor")
	var recent stringListFlag
	flag.Var(&recent, "recent-command", "recent command for local-only context (repeatable)")
	timeoutFlag := flag.Duration("timeout", clientTimeout, "foreground request timeout")
	flag.Parse()
	if *versionFlag {
		fmt.Println(version)
		return nil
	}
	if *backendFlag {
		backend, err := resolvedBackend(config)
		if err != nil {
			return err
		}
		fmt.Println(backend)
		return nil
	}
	if *backendLabelFlag {
		backend, err := resolvedBackend(config)
		if err != nil {
			return err
		}
		if backend == "local" {
			fmt.Println(config.localName)
		} else {
			fmt.Println("Codex")
		}
		return nil
	}
	if *serveFlag {
		return serve(config)
	}
	if *pingFlag {
		if err := ensureServer(config); err != nil {
			return err
		}
		fmt.Println("ready")
		return nil
	}
	promptBytes, err := io.ReadAll(io.LimitReader(os.Stdin, maxMessageSize+1))
	if err != nil {
		return err
	}
	if len(promptBytes) > maxMessageSize {
		return errors.New("prompt exceeds 1 MiB")
	}
	prompt := string(promptBytes)
	if strings.TrimSpace(prompt+*prefixFlag+*suffixFlag) == "" {
		return errors.New("prompt must be supplied on stdin")
	}
	result, err := request(config, brokerRequest{Mode: *modeFlag, Completion: CompletionRequest{WorkingDirectory: *cwdFlag, Prefix: *prefixFlag, Suffix: *suffixFlag, Prompt: prompt}, Local: LocalOnlyContext{RecentCommands: recent}}, *timeoutFlag)
	if err != nil {
		return err
	}
	_, err = fmt.Print(result)
	return err
}

type stringListFlag []string

func (values *stringListFlag) String() string         { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error { *values = append(*values, value); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "prompttab: %v\n", err)
		os.Exit(1)
	}
}
