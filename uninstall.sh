#!/usr/bin/env bash
# shellcheck disable=SC2016
set -euo pipefail

dest="${PROMPTTAB_HOME:-$HOME/.prompttab}"
if [[ -S "$dest/app-server.sock" ]]; then
    # Stop only the broker owning this exact Unix listener. Its stdio child exits on EOF.
    pid="$(lsof -t -- "$dest/app-server.sock" 2>/dev/null || true)"
    [[ -z "$pid" ]] || kill "$pid"
fi
bashrc="$HOME/.bashrc"
marker='source "$HOME/.prompttab/bashrc.sh"'
if [[ -f "$bashrc" ]]; then
    mode="$(stat -f '%Lp' "$bashrc")"
    temp_file="$(mktemp -t prompttab-bashrc.XXXXXX)"
    trap 'rm -f -- "$temp_file"' EXIT
    awk -v marker="$marker" '$0 != marker' "$bashrc" >"$temp_file"
    chmod "$mode" "$temp_file"
    mv "$temp_file" "$bashrc"
    trap - EXIT
fi
printf 'Remove %s after reviewing it. Your normal Codex configuration was not changed.\n' "$dest"
