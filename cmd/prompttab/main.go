package main

import (
	"bufio"
	"bytes"
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
	home       string
	socketPath string
	lockPath   string
	logPath    string
	codex      string
	model      string
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
	return settings{
		home:       absoluteHome,
		socketPath: filepath.Join(absoluteHome, "app-server.sock"),
		lockPath:   filepath.Join(absoluteHome, "start.lock"),
		logPath:    filepath.Join(absoluteHome, "app-server.log"),
		codex:      firstNonEmpty(os.Getenv("CODEX_BIN"), "codex"),
		model:      firstNonEmpty(os.Getenv("PROMPTTAB_MODEL"), os.Getenv("CODEX_OPTION_TAB_MODEL"), "gpt-5.6-luna"),
	}, nil
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

func generate(config settings, rpc *rpcClient, mode, prompt string) (string, error) {
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
		"input":                 []map[string]string{{"type": "text", "text": prompt}},
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

type brokerRequest struct {
	Mode   string `json:"mode"`
	Prompt string `json:"prompt"`
}

type brokerResponse struct {
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func serve(config settings) error {
	if err := os.MkdirAll(filepath.Join(config.home, "empty-workspace"), 0700); err != nil {
		return err
	}
	rpc, err := initializeRPC(config)
	if err != nil {
		return err
	}
	defer rpc.close()
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
		fatal, err := handleConnection(config, rpc, connection)
		_ = connection.Close()
		if fatal {
			return err
		}
	}
}

func handleConnection(config settings, rpc *rpcClient, connection net.Conn) (bool, error) {
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var request brokerRequest
	err := json.NewDecoder(io.LimitReader(connection, maxMessageSize+1)).Decode(&request)
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, nil
	}
	if strings.TrimSpace(request.Prompt) == "" {
		_ = json.NewEncoder(connection).Encode(brokerResponse{Error: "prompt must not be empty"})
		return false, nil
	}
	result, err := generate(config, rpc, request.Mode, request.Prompt)
	response := brokerResponse{Result: result}
	fatal := false
	if err != nil {
		response = brokerResponse{Error: err.Error()}
		fatal = true
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

func request(config settings, mode, prompt string, timeout time.Duration) (string, error) {
	if _, err := modeInstructions(mode); err != nil {
		return "", err
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
	if err := json.NewEncoder(connection).Encode(brokerRequest{Mode: mode, Prompt: prompt}); err != nil {
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

func run() error {
	config, err := loadSettings()
	if err != nil {
		return err
	}
	serveFlag := flag.Bool("serve", false, "run the local broker")
	pingFlag := flag.Bool("ping", false, "start the broker if needed and report readiness")
	versionFlag := flag.Bool("version", false, "print the PromptTab version")
	modeFlag := flag.String("mode", "command", "request mode: command or ask")
	timeoutFlag := flag.Duration("timeout", clientTimeout, "foreground request timeout")
	flag.Parse()
	if *versionFlag {
		fmt.Println(version)
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
	if strings.TrimSpace(prompt) == "" {
		return errors.New("prompt must be supplied on stdin")
	}
	result, err := request(config, *modeFlag, prompt, *timeoutFlag)
	if err != nil {
		return err
	}
	_, err = fmt.Print(result)
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "prompttab: %v\n", err)
		os.Exit(1)
	}
}
