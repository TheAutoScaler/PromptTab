# shellcheck shell=bash
# Codex AI Bash assistant. Only Control-Space invokes Codex; Tab remains untouched.
_prompttab_home="${PROMPTTAB_HOME:-${CODEX_OPTION_TAB_HOME:-$HOME/.prompttab}}"
_prompttab_bin="$_prompttab_home/bin/prompttab"

# A stale source line in .bashrc must be harmless if PromptTab is not installed.
if [[ ! -x "$_prompttab_bin" ]]; then
    unset _prompttab_home _prompttab_bin
    if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
        return 0
    fi
    exit 0
fi

_codex_ai_assist() {
    local current instruction result status ai_prompt question helper_home
    helper_home="${PROMPTTAB_HOME:-${CODEX_OPTION_TAB_HOME:-$HOME/.prompttab}}"
    current="$READLINE_LINE"

    if ! command -v bash >/dev/null 2>&1 || ! command -v stty >/dev/null 2>&1; then
        printf '\aPromptTab requires bash and stty for interactive command entry.\n' >&2
        return 1
    fi

    question="Codex › "

    # A child Bash owns its Readline interaction and restores the exact tty state.
    instruction="$(
        bash -c '
            old_tty="$(stty -g </dev/tty)" || exit 1
            cleanup() { stty "$old_tty" </dev/tty 2>/dev/null; }
            trap cleanup EXIT INT TERM HUP
            stty echo </dev/tty
            read -e -r -p "$1" answer </dev/tty
            printf "%s" "$answer"
        ' _ "$question"
    )"

    if [[ -z "${instruction//[[:space:]]/}" ]]; then
        READLINE_LINE="$current"
        READLINE_POINT=${#READLINE_LINE}
        return
    fi

    if [[ -n "${current//[[:space:]]/}" ]]; then
        ai_prompt="Current command:
$current

User wants:
$instruction"
    else
        ai_prompt="Command request:
$instruction"
    fi

    result="$(printf '%s' "$ai_prompt" | "$helper_home/bin/prompttab")"
    status=$?

    if [[ $status -eq 0 && -n "$result" ]]; then
        result="${result//$'\r'/}"
        result="${result//$'\n'/ }"
        READLINE_LINE="$result"
        READLINE_POINT=${#READLINE_LINE}
    else
        READLINE_LINE="$current"
        READLINE_POINT=${#READLINE_LINE}
        printf '\aCodex command generation failed.\n' >&2
    fi
}

# Terminals encode Control-Space as NUL, the same key sequence as Control-@.
if [[ $- == *i* ]] && command -v bind >/dev/null 2>&1; then
    bind -x '"\C-@":_codex_ai_assist'
fi

# Ask a quick question or explain a quoted command using the same local broker.
prompttab_ask() {
    local prompt result status helper_home
    helper_home="${PROMPTTAB_HOME:-${CODEX_OPTION_TAB_HOME:-$HOME/.prompttab}}"

    if [[ $# -gt 0 ]]; then
        prompt="$*"
    else
        read -e -r -p "Codex › " prompt
    fi

    [[ -n "${prompt//[[:space:]]/}" ]] || return 0

    result="$(printf '%s' "$prompt" | "$helper_home/bin/prompttab" --mode ask)"
    status=$?
    if [[ $status -eq 0 ]]; then
        printf '%s\n' "$result"
    else
        printf '\aCodex request failed.\n' >&2
        return "$status"
    fi
}

# Defined in this startup file so alias expansion occurs before pathname expansion.
alias '?'=prompttab_ask

# Warm only the local broker/app-server process. No command text or model request is sent.
if [[ "${PROMPTTAB_EAGER_START:-${CODEX_OPTION_TAB_EAGER_START:-1}}" == 1 ]]; then
    "$_prompttab_bin" --ping >/dev/null 2>&1
fi

unset _prompttab_home _prompttab_bin
