#!/usr/bin/env bash
# Build and install mail-api on the VPS. Config in /etc/mail-api/ is left alone.
set -euo pipefail
HOST=${HOST:?set HOST, e.g. HOST=user@server ./deploy.sh}
SSH_KEY=${SSH_KEY:-$HOME/.ssh/id_ed25519_nopass}
cd "$(dirname "$0")"
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/mail-api .
scp -i "$SSH_KEY" dist/mail-api deploy/mail-api.service "$HOST:/tmp/"
ssh -i "$SSH_KEY" "$HOST" '
  set -e
  sudo install -m 0755 /tmp/mail-api /usr/local/bin/mail-api
  sudo install -m 0644 /tmp/mail-api.service /etc/systemd/system/mail-api.service
  rm /tmp/mail-api /tmp/mail-api.service
  sudo systemctl daemon-reload
  sudo systemctl restart mail-api
  sleep 1
  systemctl is-active mail-api
  curl -fsS http://127.0.0.1:5300/mail/health
'
