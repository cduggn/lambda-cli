#!/usr/bin/env bash
# Symlink ./lam into ~/.local/bin and create the config file if missing.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$HOME/.local/bin"
ln -sf "$here/lam" "$HOME/.local/bin/lam"
echo "linked ~/.local/bin/lam -> $here/lam"
case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) echo "add to PATH:  export PATH=\"\$HOME/.local/bin:\$PATH\"";; esac
for t in curl jq perl ssh; do command -v "$t" >/dev/null || echo "missing: $t"; done
[[ -f "$HOME/.config/lam/config" ]] || "$here/lam" config init
"$here/lam" config
