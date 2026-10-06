#!/usr/bin/env bash
# Install (or update) the global graph query service from this directory.
# Copies rather than symlinks, so the service never depends on a checkout's
# current branch or uncommitted state.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
dest="$HOME/.local/share/ggraphify/globalquery"

install -D -m 0644 "$here/ggserve.py" "$dest/ggserve.py"
install -D -m 0755 "$here/ggq" "$HOME/.local/bin/ggq"
install -D -m 0644 "$here/ggraphify-global-query.service" \
  "$HOME/.config/systemd/user/ggraphify-global-query.service"

systemctl --user daemon-reload
systemctl --user enable ggraphify-global-query >/dev/null
systemctl --user restart ggraphify-global-query
echo "installed; waiting for the first load (~30 s) — check with: ggq health"
