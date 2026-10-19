#!/bin/bash
# M3 check: pair a client, read history, subscribe — with nothing but curl.
#   sysqd &            (or: sysqd --fixtures builtin &)
#   scripts/curl-demo.sh
set -euo pipefail
API="${SYSQ_API:-http://127.0.0.1:11435}"
SOCK="${SYSQ_ADMIN:-$HOME/Library/Application Support/sysq/admin.sock}"
j() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }

echo "1. first contact returns 401 with a pairing code"
first="$(curl -s -H 'X-Sysq-Client: curl demo' "$API/v1/status")"
echo "$first"
id="$(echo "$first" | j "['pairing']['id']")"
code="$(echo "$first" | j "['pairing']['code']")"

echo "2. the host approves it (Headroom does this from its pairing UI)"
curl -s --unix-socket "$SOCK" -X POST http://admin/admin/approve \
  -d "{\"id\":\"$id\",\"code\":\"$code\",\"privacy\":\"consumers\"}" >/dev/null

echo "3. the client collects its token, once"
token="$(curl -s "$API/v1/pair/$id" | j "['token']")"

echo "4. read history"
curl -s -H "Authorization: Bearer $token" "$API/v1/history/mem.headline?range=10m" | head -c 600; echo

echo "5. subscribe (SSE) for ~8 seconds"
curl -s -N --max-time 8 "$API/v1/subscribe/mem.headline?every=2s&token=$token" | head -c 800 || true
echo
echo "done"
