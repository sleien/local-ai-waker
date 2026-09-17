#!/usr/bin/env bash
# Checks the OpenAI-compatible endpoint the way a client sees it. Worth running
# once while the workstation sleeps: the model list must not wake it, the chat
# request must.
#
#   scripts/smoke-openai.sh https://ai.example.ch/v1 "$WAKER_API_KEY" qwen2.5:32b
set -euo pipefail
export LC_ALL=C

base="${1:?base URL, e.g. https://ai.example.ch/v1}"
key="${2:?API key (WAKER_API_KEY)}"
model="${3:?model name as Ollama lists it, e.g. qwen2.5:32b}"
auth="Authorization: Bearer $key"

now() { echo "${EPOCHREALTIME:-$(date +%s)}"; }
since() { awk -v a="$1" -v b="$(now)" 'BEGIN { printf "%6.2fs", b - a }'; }

echo "== GET /models"
headers=$(curl -sS -o /dev/null -D - -H "$auth" "$base/models" | tr -d '\r')
echo "$headers" | grep -iE '^(HTTP/|x-waker)'
if echo "$headers" | grep -qi '^x-waker-cached: 1'; then
	echo "   -> answered from cache, workstation left asleep"
elif echo "$headers" | grep -q '^HTTP/[0-9.]* 200'; then
	echo "   -> answered live by the workstation"
fi

echo
echo "== POST /chat/completions (stream), timestamps show tokens arriving live"
payload=$(printf '{"model":"%s","stream":true,"messages":[{"role":"user","content":"Count from 1 to 20, comma separated."}]}' "$model")
start=$(now)
curl -sSN -D /dev/stderr -H "$auth" -H 'Content-Type: application/json' -d "$payload" "$base/chat/completions" 2> >(grep -iE '^(HTTP/|x-waker)' | tr -d '\r' >&2) |
	while IFS= read -r line; do
		[ -z "$line" ] && continue
		content=$(printf '%s' "$line" | sed -n 's/.*"delta":{[^}]*"content":"\([^"]*\)".*/\1/p')
		printf '%s  %s\n' "$(since "$start")" "${content:-${line:0:100}}"
	done
