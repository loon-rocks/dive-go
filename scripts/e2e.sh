#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
pid=""

cleanup() {
  if [ -n "$pid" ]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

cp -R "$root/testdata/e2e/." "$work/project"
mkdir -p "$work/home"
(
  cd "$work/project"
  HOME="$work/home" DIVE_STORAGE=memory exec dive serve --dev-key --listen "127.0.0.1:$port"
) >"$work/serve.log" 2>&1 &
pid=$!

for _ in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:$port/healthz" >/dev/null; then
    break
  fi
  sleep 0.2
done
curl -sf "http://127.0.0.1:$port/healthz" >/dev/null || { cat "$work/serve.log"; exit 1; }

cd "$root"
DIVE_E2E_URL="http://127.0.0.1:$port" \
DIVE_DEV_KEY="$work/home/.local/share/dive/serve/dev-key.pem" \
  go test -race -count=1 -run TestAgainstDiveServe -v .
