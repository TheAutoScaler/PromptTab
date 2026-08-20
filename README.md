# PromptTab

Explicit, privacy-first AI command completion for Bash on macOS, powered by a
persistent Codex app-server.

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

Only the current editable command line, when non-empty, and the request entered at
the local `Codex › ` prompt are placed in a command-completion request.

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
Unix socket `~/.prompttab/app-server.sock`; that broker owns one persistent
`codex app-server --stdio` child. Later shells reuse it. This small broker is needed
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

Only Control-Space invokes PromptTab. Ordinary typing, Tab, Enter, shell history,
terminal output, environment variables, repositories, and filesystem contents are
not collected. Please report security issues privately rather than opening a
public issue containing exploit details.

## Compatibility

`codex app-server` is currently experimental. PromptTab negotiates and validates
the protocol exposed by Codex CLI 0.147.0. Re-run `verify.sh` after upgrading Codex.

## License

Apache License 2.0. See [LICENSE](LICENSE).
