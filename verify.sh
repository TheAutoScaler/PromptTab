#!/usr/bin/env bash
set -euo pipefail

home="${PROMPTTAB_HOME:-${CODEX_OPTION_TAB_HOME:-$HOME/.prompttab}}"
export CODEX_HOME="$home"

printf 'Authentication: '
codex login status
printf '\nMCP configuration:\n'
codex mcp list
printf '\nPlugins:\n'
codex plugin list --json
printf '\nConfigured tool-related feature flags (all must be false):\n'
sed -n '/^\[features\]/,$p' "$home/config.toml"
printf '\nServer: '
"$home/bin/prompttab" --ping

printf '\nQuestion/explanation mode:\n'
printf '%s' 'What is 2 + 2?' | "$home/bin/prompttab" --mode ask
printf '\n'

test_path="/tmp/PROMPTTAB_EXECUTION_TEST"
if [[ -e "$test_path" ]]; then
    printf '%s already exists; remove it manually before the insertion-only test.\n' "$test_path" >&2
    exit 1
fi
printf '\nAutomatic-execution test:\n'
printf '1. At an interactive Bash prompt, press Control-Space.\n'
printf '2. Enter: Return exactly touch /tmp/PROMPTTAB_EXECUTION_TEST\n'
printf '3. BEFORE pressing Enter at the outer prompt, run in another terminal:\n'
printf '   test ! -e /tmp/PROMPTTAB_EXECUTION_TEST && echo PASS\n'
printf '4. Only pressing Enter yourself may create the file.\n'

printf '\nStatelessness test:\n'
printf 'Run two Control-Space requests. Each helper call creates a new ephemeral thread ID;\n'
printf 'request B is never sent through request A thread and history persistence is none.\n'
