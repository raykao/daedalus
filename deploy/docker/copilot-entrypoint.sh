#!/bin/bash
set -euo pipefail

# Validate GITHUB_TOKEN is set
if [ -z "$GITHUB_TOKEN" ]; then
    echo "ERROR: GITHUB_TOKEN environment variable is required" >&2
    exit 1
fi

# Detect the copilot binary name (varies by package version)
COPILOT_BIN=""
for candidate in copilot github-copilot copilot-cli; do
    if command -v "$candidate" &>/dev/null; then
        COPILOT_BIN="$candidate"
        break
    fi
done

if [ -z "$COPILOT_BIN" ]; then
    echo "ERROR: No copilot CLI binary found in PATH" >&2
    echo "Installed npm global binaries:" >&2
    ls /usr/local/lib/node_modules/ 2>/dev/null || true
    npm list -g --depth=0 2>/dev/null || true
    exit 1
fi

echo "Starting Copilot CLI in ACP mode: $COPILOT_BIN --acp $@"
exec "$COPILOT_BIN" --acp "$@"
