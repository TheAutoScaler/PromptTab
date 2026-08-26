# PromptTab

Explicit, privacy-first AI command completion for Bash on macOS, powered by a
persistent Codex app-server or a resident local `llama-server`.

PromptTab is designed for use with the Codex CLI:

- **Lower latency:** it keeps its own persistent local Codex app-server running, so
  each request does not need to start a new Codex process.
- **Privacy focused:** it sends requests only when you explicitly invoke it. Normal
  typing, Tab, Enter, history, terminal output, environment variables, and filesystem
  contents are not sent to OpenAI.
- **Command completion:** press `Control-Space` to complete a partial command or
  describe an entirely new one. The result is inserted into your command line for
  review and is never executed automatically.
- **Quick Codex queries:** use `?` to ask a general question or explain a shell
  command without executing it.
- **Fully local completion:** optionally use a small FIM-capable GGUF model for
  low-latency completion of the text at the cursor. Quick questions remain on Codex.

Only the current editable command line, when non-empty, and the request entered at
the local `Codex › ` prompt are placed in a command-completion request.

## Use PromptTab with a local model

llama.cpp is a program that runs an AI model on your Mac. PromptTab can use it to
finish commands without sending them to Codex. Quick questions still use Codex.

First, install llama.cpp:

```bash
brew install llama.cpp
```

Then, start a local coding model:

```bash
llama-server \
  -hf MaziyarPanahi/Qwen2.5-Coder-1.5B-GGUF:Q5_K_M \
  --host 127.0.0.1 --port 8012 \
  --n-gpu-layers 99 --ctx-size 2048 --parallel 1 --cache-reuse 256
```

After you [install PromptTab](#install), open `~/.prompttab/prompttab.toml` and
change the first line to:

```toml
backend = "auto"
```

Source `~/.bashrc`, type part of a command such as `git status --`, and press
Control-Space. PromptTab uses the local model when the server is running and Codex
when it is not. See [Local llama.cpp completion](#local-llamacpp-completion) for
other models and settings.

## Requirements

- macOS with Bash and Readline
- Codex CLI 0.147.0 or a protocol-compatible release
- An existing ChatGPT-authenticated Codex login (`codex login status`)

PromptTab does not use Python or an OpenAI API key. The installer uses a prebuilt
native binary when Go is not installed.

## Quick questions and command explanations

The `?` alias uses the same persistent, isolated server for concise general
questions:

```bash
? why does DNS primarily use UDP
```

If the input looks like a shell command, PromptTab explains it without executing
it:

```bash
? 'find . -type f -print0 | xargs -0 du -h | sort -hr | head'
```

Quote commands containing pipes, redirects, semicolons, substitutions, or other
shell syntax. Otherwise, Bash will interpret that syntax before PromptTab can
receive it. Calling `?` without arguments displays the `Codex › ` prompt.

Question and explanation requests are explicit, stateless, and use fresh ephemeral
threads. Their answers are printed as text and are never evaluated or executed.

## Keyboard shortcut

```text
Tab            Normal local Bash completion
Control-Space  PromptTab
```

Terminals encode Control-Space as the same NUL key sequence as Control-@. PromptTab
therefore replaces Readline's default `set-mark` binding for that key. If macOS uses
Control-Space for input-source switching, disable or remap that shortcut under
System Settings → Keyboard → Keyboard Shortcuts → Input Sources.

## Install

```bash
git clone git@github.com:TheAutoScaler/PromptTab.git
cd PromptTab
./install.sh
source "$HOME/.bashrc"
```

The installer creates `~/.prompttab` and adds one exact `source` line to
`~/.bashrc`. It builds the binary from the checkout when Go is available; otherwise,
it downloads the matching Apple Silicon or Intel binary from GitHub Releases. It
symlinks `~/.prompttab/auth.json` to the existing
`~/.codex/auth.json`: authentication is shared, while configuration, state,
threads, plugins, skills, and MCP configuration remain isolated. No secret is
copied. If Codex changes its auth storage format, rerun `codex login` normally and
check `CODEX_HOME=~/.prompttab codex login status`.

To keep shell startup under separate version control or install somewhere else,
disable `.bashrc` management and source the installed startup file yourself:

```bash
PROMPTTAB_HOME="$HOME/.local/share/prompttab" \
PROMPTTAB_MANAGE_BASHRC=0 \
    ./install.sh
```

Your shell configuration must export the same `PROMPTTAB_HOME` value before
sourcing `$PROMPTTAB_HOME/bashrc.sh`.

## Local llama.cpp completion

The short setup above uses **Qwen2.5-Coder-1.5B Base** in Q5_K_M format. It is a
good place to start on an M3 Mac with 24 GB of memory. A 0.5B model is faster. A 3B
model may give better answers but takes longer. Use a FIM-capable GGUF model so it
can complete text at the cursor. llama.cpp downloads and caches the chosen model
from Hugging Face.

Keep `llama-server` running while you use local completion. PromptTab connects to
it but does not start or stop it. The default config is:

```toml
backend = "auto"

[local]
name = "Qwen2.5 Coder 1.5B"
url = "http://127.0.0.1:8012"
max_tokens = 32
temperature = 0
timeout_ms = 750
```

The installer copies this config to `~/.prompttab/prompttab.toml` and keeps an
existing copy unchanged. `local.name` is the name shown by the Control-Space
prompt.

With `backend = "auto"`, PromptTab checks the configured URL when its broker starts
and every 10 seconds after that. It uses the local model when `GET /health` works
and Codex when it does not. It checks only that URL. It does not scan your network.
Use `backend = "local"` to require the local server, or `backend = "codex"` to
require Codex.

In local mode, PromptTab inserts the completion at the cursor. Text after the
cursor helps the model complete the middle of a command. The URL defaults to
`http://127.0.0.1:8012`. You can use `PROMPTTAB_BACKEND=local` to change the backend
for one shell session. Run this to see which backend PromptTab is using:

```bash
~/.prompttab/bin/prompttab --backend
```

A llama-server normally loads one model. To switch models, restart it with another
model. You can also run servers on different ports and change `local.url`.

Local completion may use the working directory and up to eight recent commands for
context. This recent command data stays on your computer and is never sent to
Codex/OpenAI, even when local completion fails. There is no automatic Codex retry
after a failed local completion.

## Test without installing

This stages the isolated environment in `/tmp` and changes only the current Bash
process. It does not edit `~/.bashrc`:

```bash
src="$(pwd)"
test_home="/tmp/prompttab-$UID"
mkdir -p "$test_home/bin" "$test_home/empty-workspace"
cp "$src/config.toml" "$test_home/config.toml"
go build -trimpath -o "$test_home/bin/prompttab" ./cmd/prompttab
chmod 700 "$test_home/bin/prompttab"
ln -sfn "$HOME/.codex/auth.json" "$test_home/auth.json"
export PROMPTTAB_HOME="$test_home"
source "$src/bashrc.sh"
```

Press Control-Space to test. When finished, exit that Bash session. Optionally stop
the temporary broker and delete only the explicit temporary directory:

```bash
pid="$(lsof -t -- "$PROMPTTAB_HOME/app-server.sock" 2>/dev/null || true)"
[[ -z "$pid" ]] || kill "$pid"
rm -rf -- "$PROMPTTAB_HOME"
```

The first invocation starts one per-user local broker bound only to the private
Unix socket `~/.prompttab/app-server.sock`. In Codex mode that broker owns one
persistent `codex app-server --stdio` child; in local mode it reuses one HTTP client
to contact the separately resident llama-server. Later shells reuse the broker. The
local and automatic backends use separate Unix sockets, so changing backend cannot
reuse a broker configured for another provider mode. This small broker is needed
because the Homebrew Codex CLI's built-in managed daemon requires the separate
standalone Codex installation. A dead broker/server is restarted on the next
invocation. Logs are in `~/.prompttab/app-server.log`.

Each request starts a new `ephemeral` thread with history persistence disabled,
empty runtime workspace roots, empty environments, no dynamic tools, a fixed empty
working directory, and zero project-instruction bytes. Model output is handled as
text, with CR/LF removed in both the client and Bash callback. Nothing calls
`eval`, `source` on generated text, `accept-line`, a shell executor, or simulated
Enter.

## Verify

```bash
~/.prompttab/verify.sh
```

Or, from this source directory before installation, inspect `verify.sh`. The
installed CLI's generated app-server protocol exposes no universal per-turn
"tools: []" field for built-in tools. This package therefore disables every
relevant tool feature in strict config, provides no MCP/plugins/dynamic tools,
removes environments/workspace roots, uses read-only + never-approve, rejects any
server-side approval/tool request, and fails if a tool-completion item is observed.
That is strong defense in depth, but it is not a formal protocol-level proof that
the server advertised an empty built-in tool schema. Re-check after Codex upgrades.

To inspect the model-visible request construction on a compatible CLI, generate
the matching schema with:

```bash
codex app-server generate-json-schema --experimental --out /tmp/codex-schema
```

## Latency

The isolated configuration uses Luna with low reasoning effort and low output
verbosity. Sourcing `bashrc.sh` eagerly starts only the local broker/app-server, so
the first Control-Space does not pay process startup cost; it sends no command text or
model request. Set `PROMPTTAB_EAGER_START=0` before sourcing to disable this.

No timing line is printed into the terminal UI. A model turn is capped at 20
seconds; on timeout the broker exits after reporting failure, allowing the next
Control-Space invocation to start a clean app-server instead of queuing forever behind
the failed turn.

The isolated configuration uses Luna's `priority` Fast tier by default. The model
itself can still be selected before the broker starts:

```bash
export PROMPTTAB_MODEL=gpt-5.6-luna
```

The model override is not required. Local warm-request measurements were approximately
2.44 seconds for Luna/default and 2.35 seconds for Luna/priority at the median;
priority had greater tail variance. GPT-5.4 Mini was slower in the same test. Luna
with priority is now the default.

## Rollback

```bash
./uninstall.sh
```

The script stops the exact Unix-socket listener and removes only its own `.bashrc`
source line. It deliberately asks you to review and remove
`~/.prompttab` yourself, so auth/state cleanup is not destructive. Your
normal `~/.codex/config.toml` is never edited.

## Build and release

Build locally with Go 1.22 or newer:

```bash
go test ./...
go build -o bin/prompttab ./cmd/prompttab
```

The GitHub Actions release workflow tests and cross-compiles native
`darwin/arm64` and `darwin/amd64` archives. Pushing a tag such as `v0.1.0` creates
a GitHub Release containing both archives and `SHA256SUMS`:

```bash
git tag v0.1.0
git push origin v0.1.0
```

## Security and privacy

PromptTab treats model output exclusively as untrusted text. It strips carriage
returns and newlines, assigns the result only to `READLINE_LINE`, and never calls
`eval`, `source`, `bash -c`, `accept-line`, or simulated Enter on generated output.

Only Control-Space invokes PromptTab. Ordinary typing, Tab, and Enter do not invoke
it. The local backend can receive the current working directory, current cursor
contents, and a small recent-command window. Codex/OpenAI can receive the working
directory and current editable command line, but never receives recent commands or
shell history. Terminal output, environment variables, repositories, and filesystem
contents are not collected. Please report security issues privately rather than
opening a public issue containing exploit details.

## Compatibility

`codex app-server` is currently experimental. PromptTab negotiates and validates
the protocol exposed by Codex CLI 0.147.0. Re-run `verify.sh` after upgrading Codex.

## License

Apache License 2.0. See [LICENSE](LICENSE).
