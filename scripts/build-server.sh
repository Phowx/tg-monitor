#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

GO="${GO:-go}"
mkdir -p dist

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -ldflags='-s -w' -o dist/tg-monitor-server-linux-amd64 ./cmd/tg-monitor-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$GO" build -trimpath -ldflags='-s -w' -o dist/tg-monitor-server-linux-arm64 ./cmd/tg-monitor-server
