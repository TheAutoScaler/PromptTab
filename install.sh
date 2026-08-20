#!/usr/bin/env bash
# shellcheck disable=SC2016
set -euo pipefail

src="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
dest="${PROMPTTAB_HOME:-$HOME/.prompttab}"
manage_bashrc="${PROMPTTAB_MANAGE_BASHRC:-1}"
case "$manage_bashrc" in
    0|1) ;;
    *)
        printf 'PROMPTTAB_MANAGE_BASHRC must be 0 or 1.\n' >&2
        exit 1
        ;;
esac

mkdir -p "$dest/bin" "$dest/empty-workspace"
chmod 700 "$dest" "$dest/bin" "$dest/empty-workspace"
install -m 600 "$src/config.toml" "$dest/config.toml"
install -m 600 "$src/bashrc.sh" "$dest/bashrc.sh"
install -m 700 "$src/verify.sh" "$dest/verify.sh"
install -m 700 "$src/uninstall.sh" "$dest/uninstall.sh"

install_binary() {
    local arch asset release_base release_url temp_dir

    if [[ -x "$src/bin/prompttab" ]]; then
        install -m 700 "$src/bin/prompttab" "$dest/bin/prompttab"
        return
    fi

    if command -v go >/dev/null 2>&1; then
        (
            cd "$src"
            go build -trimpath -ldflags '-s -w -X main.version=source' \
                -o "$dest/bin/prompttab" ./cmd/prompttab
        )
        chmod 700 "$dest/bin/prompttab"
        return
    fi

    case "$(uname -m)" in
        arm64)  arch="arm64" ;;
        x86_64) arch="amd64" ;;
        *) printf 'Unsupported macOS architecture: %s\n' "$(uname -m)" >&2; exit 1 ;;
    esac

    asset="prompttab-darwin-$arch.tar.gz"
    if [[ -n "${PROMPTTAB_VERSION:-}" ]]; then
        release_base="https://github.com/TheAutoScaler/PromptTab/releases/download/${PROMPTTAB_VERSION}"
    else
        release_base="https://github.com/TheAutoScaler/PromptTab/releases/latest/download"
    fi
    release_url="$release_base/$asset"
    temp_dir="$(mktemp -d -t prompttab-install.XXXXXX)"
    curl -fsSL "$release_url" -o "$temp_dir/$asset"
    curl -fsSL "$release_base/SHA256SUMS" -o "$temp_dir/SHA256SUMS"
    (
        cd "$temp_dir"
        grep -F "  ./$asset" SHA256SUMS >EXPECTED_SHA256
        shasum -a 256 -c EXPECTED_SHA256
    )
    tar -xzf "$temp_dir/$asset" -C "$temp_dir"
    install -m 700 "$temp_dir/prompttab" "$dest/bin/prompttab"
    rm -rf -- "$temp_dir"
}

install_binary

if [[ ! -e "$HOME/.codex/auth.json" ]]; then
    printf 'No normal Codex auth found. Run `codex login`, then rerun this installer.\n' >&2
    exit 1
fi
ln -sfn "$HOME/.codex/auth.json" "$dest/auth.json"

case "$manage_bashrc" in
    1)
        marker='source "$HOME/.prompttab/bashrc.sh"'
        if ! grep -Fqx "$marker" "$HOME/.bashrc" 2>/dev/null; then
            printf '\n%s\n' "$marker" >>"$HOME/.bashrc"
        fi
        ;;
    0) ;;
esac

"$dest/bin/prompttab" --ping
if [[ "$manage_bashrc" == 1 ]]; then
    printf 'Installed. Run: source "$HOME/.bashrc"\n'
else
    printf 'Installed without editing ~/.bashrc. Source %s/bashrc.sh from your shell configuration.\n' "$dest"
fi
