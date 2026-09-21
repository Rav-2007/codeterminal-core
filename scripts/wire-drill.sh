#!/usr/bin/env bash
#
# wire-drill.sh — what a VS Code user actually receives when an edit is applied
# over the socket.
#
# Usage: scripts/wire-drill.sh [workdir]     (default: a mktemp -d)
#
# WHAT THIS IS
#
# Three surfaces apply edits. The CLI and the TUI call editapply.PrepareEdit in
# their own process, so they have always had the syntax note, the match note and
# the list of blocks the parser refused. VS Code is the only surface that applies
# over the SOCKET, and until 2026-08-30 the socket computed all of that, gated on
# it, and dropped it at the encode.
#
# Commit a9c0a2a added the wire fields. Commit 37a9bb2 added Tier B, the
# advisory delimiter check for TypeScript, JavaScript and Python. Both are
# covered by unit tests and by neuter matrices, and NEITHER had ever been driven
# end to end: no test in this repository starts a real daemon, connects a real
# client to a real Unix socket, and reads what comes back. The Sequence D plan
# asked for exactly that and it was not done, so this is the thing that was owed.
#
# WHAT IT PROVES THAT `go test` CANNOT
#
# editapply's tests call the gate directly. daemon's tests call the handler
# directly. Both skip the encode/decode boundary, which is precisely where the
# fields were being lost — the bug was never in a gate, it was in what the
# server chose to put in the struct it wrote to the wire. Only a real socket
# round-trip can see that, and it is the same reason sigterm-drill.sh exists
# next to proxy/integration_test.go.
#
# The model is faked, deliberately: a local HTTP server returns a canned SSE
# stream. The DAEMON is real, the socket is real, the gates are real, the parse
# is real, and the JSON on the wire is read as bytes. What is stubbed is the one
# component whose output this drill is not testing.
#
# EXPECTED OUTPUT (all four PASS):
#
#   1. .ts unbalanced   applied=true   syntax_note="delimiters unbalanced: unclosed
#                      "{" opened at line 1 - ADVISORY ONLY, the edit was applied;
#                      typescript is not parsed by this build, so this is a bracket
#                      count and not a syntax error"
#   2. .go unparseable  applied=false  error="edit would make main.go unparseable as
#                      Go: main.go:6:18: missing ',' before newline ..."
#   3. one good, one bad   edit_proposals=1  edit_rejections=1
#                          reason="line 11: unterminated block (no \">>>>>>> REPLACE\"
#                          found before end of response)"
#   4. all blocks bad      edit_proposals=0  edit_rejections=1   <- used to send NOTHING
#
#   wire-drill: 7 passed, 0 failed
#
# Row 4 is the one worth staring at. Before a9c0a2a a reply whose edits were all
# malformed returned nil and the client was sent no edit message at all, so a
# user watched a response that visibly contained edit blocks produce absolutely
# nothing, with the reason in a daemon log they cannot read.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="${1:-$(mktemp -d)}"
mkdir -p "$WORKDIR"

# XDG_RUNTIME_DIR ends up inside sockaddr_un.sun_path, which is a fixed 104-108
# byte array. A mktemp path under /tmp plus the daemon's own subdirectory and
# socket name fits; a deep one does not, and the failure is an opaque "invalid
# argument" from bind(2). Kept short on purpose.
export XDG_RUNTIME_DIR="$WORKDIR/rt"
mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"

GO="${GO:-go}"
if ! command -v "$GO" >/dev/null 2>&1; then
  for candidate in "$HOME/.local/go/bin/go" /usr/local/go/bin/go; do
    [ -x "$candidate" ] && GO="$candidate" && break
  done
fi

PASS=0
FAIL=0
say()  { printf '%s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m  %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m  %s\n' "$*"; }

cleanup() {
  [ -n "${DAEMON_PID:-}" ] && kill "$DAEMON_PID" 2>/dev/null || true
  [ -n "${FAKE_PID:-}" ] && kill "$FAKE_PID" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

say "wire-drill: workdir $WORKDIR"

# ---------------------------------------------------------------- the workspace
WS="$WORKDIR/ws"
mkdir -p "$WS"
git -C "$WS" init -q 2>/dev/null || true

# A .ts file that currently BALANCES. Tier B's delta rule only reports an
# advisory when the edit is what unbalanced it, so the starting state matters.
cat > "$WS/app.ts" <<'TS'
export function greet(name: string): string {
  return `hello ${name}`;
}
TS

# A .go file that currently PARSES, for the same reason on the Tier A side: the
# gate refuses only an edit that breaks a file which was fine before.
cat > "$WS/main.go" <<'GO'
package main

import "fmt"

func main() {
	fmt.Println("hi")
}
GO

cat > "$WS/models.json" <<'JSON'
{
  "config_version": 1,
  "default_tier": "primary",
  "tiers": {
    "primary": { "slug": "fake/model", "active": true, "note": "the canned SSE stream this drill serves" }
  },
  "zdr": { "allow_non_zdr": false, "allow_data_collection": false, "allow_fallbacks": true }
}
JSON

# ------------------------------------------------------------- the fake provider
# Serves one canned SSE stream per request, chosen by a file the drill rewrites
# between cases. Real HTTP, real SSE framing, real chunking -- the daemon's
# provider.go parses this exactly as it parses OpenRouter.
cat > "$WORKDIR/fakeprovider.py" <<'PY'
import http.server, json, os, sys, threading

REPLY_FILE = sys.argv[1]
PORT_FILE = sys.argv[2]

class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        self.rfile.read(length)
        with open(REPLY_FILE) as f:
            content = f.read()
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.end_headers()
        # One chunk per line keeps the SSE framing honest without making the
        # daemon's scanner deal with an artificially huge single chunk.
        for line in content.splitlines(keepends=True):
            chunk = {"choices": [{"index": 0, "delta": {"content": line}, "finish_reason": None}]}
            self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
            self.wfile.flush()
        final = {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}
        self.wfile.write(b"data: " + json.dumps(final).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def log_message(self, *a):
        pass

srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
with open(PORT_FILE, "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY

REPLY="$WORKDIR/reply.txt"
PORTFILE="$WORKDIR/port"
: > "$REPLY"
python3 "$WORKDIR/fakeprovider.py" "$REPLY" "$PORTFILE" &
FAKE_PID=$!
for _ in $(seq 1 50); do [ -s "$PORTFILE" ] && break; sleep 0.1; done
PORT="$(cat "$PORTFILE")"
say "wire-drill: fake provider on 127.0.0.1:$PORT"

# ------------------------------------------------------------------- the daemon
say "wire-drill: building the daemon"
DAEMON_BIN="$WORKDIR/mochiii-daemon"
(cd "$REPO_ROOT/daemon" && "$GO" build -o "$DAEMON_BIN" .)

# --no-context: retrieval needs a built index, and this drill is about the edit
# wire, not about what gets retrieved. Nothing below depends on context.
MOCHIII_API_BASE="http://127.0.0.1:$PORT" \
MOCHIII_API_KEY="drill" \
  "$DAEMON_BIN" --workspace "$WS" --config "$WS/models.json" --no-context \
  > "$WORKDIR/daemon.log" 2>&1 &
DAEMON_PID=$!

SOCK=""
for _ in $(seq 1 100); do
  SOCK="$(find "$XDG_RUNTIME_DIR" -name '*.sock' 2>/dev/null | head -1)"
  [ -n "$SOCK" ] && break
  kill -0 "$DAEMON_PID" 2>/dev/null || { say "daemon exited early:"; cat "$WORKDIR/daemon.log"; exit 1; }
  sleep 0.1
done
if [ -z "$SOCK" ]; then
  say "wire-drill: no socket appeared. daemon log:"; cat "$WORKDIR/daemon.log"; exit 1
fi
say "wire-drill: daemon on $SOCK"

# ------------------------------------------------------------------- the client
# Newline-delimited JSON over the Unix socket, which is what json.Encoder writes
# and json.Decoder reads on the daemon side. Nothing here imports the project's
# own protocol package: a client that shared the server's struct definitions
# could not catch a field that is never marshalled.
cat > "$WORKDIR/client.py" <<'PY'
import json, socket, sys

sock_path, mode = sys.argv[1], sys.argv[2]
payload = json.loads(sys.argv[3])

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(60)
s.connect(sock_path)
f = s.makefile("rwb")

f.write((json.dumps({"protocol_version": 1, "client_name": "wire-drill"}) + "\n").encode())
f.flush()
hs = json.loads(f.readline())
if not hs.get("ok"):
    print(json.dumps({"_error": "handshake refused", "hs": hs})); sys.exit(1)

f.write((json.dumps(payload) + "\n").encode())
f.flush()

if mode == "apply":
    print(f.readline().decode().strip())
else:
    # Stream every frame until the Done one, then print it. The rejections and
    # proposals ride on that terminal frame.
    last = None
    while True:
        line = f.readline()
        if not line:
            break
        msg = json.loads(line)
        if msg.get("done"):
            last = msg
            break
    print(json.dumps(last) if last else json.dumps({"_error": "stream ended with no done frame"}))
PY

apply_edit() { python3 "$WORKDIR/client.py" "$SOCK" apply "$1"; }
prompt()     { python3 "$WORKDIR/client.py" "$SOCK" prompt "$1"; }
field()      { python3 -c 'import json,sys; d=json.loads(sys.stdin.read() or "{}"); print(d.get(sys.argv[1], ""))' "$1"; }

say ""
say "=== 1. Tier B: a .ts edit that unbalances braces must APPLY, with an advisory ==="
RESP="$(apply_edit "$(python3 - <<'PY'
import json
print(json.dumps({
  "protocol_version": 1,
  "edit": {
    "file_path": "app.ts",
    "search": "  return `hello ${name}`;\n}",
    "replace": "  if (name) {\n    return `hello ${name}`;\n}",
  },
}))
PY
)")"
say "  $RESP"
APPLIED="$(printf '%s' "$RESP" | field applied)"
NOTE="$(printf '%s' "$RESP" | field syntax_note)"
if [ "$APPLIED" = "True" ]; then
  ok "applied=true — Tier B did not refuse, which is its entire design"
else
  bad "applied=$APPLIED — Tier B REFUSED a TypeScript edit. It must never refuse."
fi
if [ -z "$NOTE" ]; then
  bad "syntax_note is EMPTY on the wire — the field is not being populated"
elif printf '%s' "$NOTE" | grep -qi 'unbalanced' && printf '%s' "$NOTE" | grep -qi 'advisory'; then
  # Both words, and neither is decorative: "unbalanced" is the finding, and
  # "advisory" is the promise that the finding did not block the edit. A note
  # carrying one without the other would be a different contract.
  ok "syntax_note reports the imbalance AND marks itself advisory:"
  say "        $NOTE"
else
  bad "syntax_note is missing 'unbalanced' or 'advisory': $NOTE"
fi

say ""
say "=== 2. Tier A: a .go edit that breaks the parse must still REFUSE ==="
RESP="$(apply_edit "$(python3 - <<'PY'
import json
print(json.dumps({
  "protocol_version": 1,
  "edit": {
    "file_path": "main.go",
    "search": "\tfmt.Println(\"hi\")\n}",
    "replace": "\tfmt.Println(\"hi\"\n",
  },
}))
PY
)")"
say "  $RESP"
APPLIED="$(printf '%s' "$RESP" | field applied)"
ERR="$(printf '%s' "$RESP" | field error)"
# BOTH halves, because the first draft of this check asserted only applied=false
# and PASSED on a run where the edit never reached the gate at all: a malformed
# request was refused for naming no file, and "refused" looked like success. A
# refusal proves nothing unless it is the refusal you asked for.
if [ "$APPLIED" != "False" ]; then
  bad "applied=$APPLIED — an unparseable .go edit was APPLIED. Tier A has regressed."
elif printf '%s' "$ERR" | grep -qiE 'syntax|parse|expected'; then
  ok "applied=false, and it is the SYNTAX gate that said so: $ERR"
else
  bad "applied=false but for the wrong reason — nothing here mentions syntax: $ERR"
fi

say ""
say "=== 3. One good block and one malformed one ==="
cat > "$REPLY" <<'EOF'
Here are two edits.

path: app.ts
<<<<<<< SEARCH
export function greet(name: string): string {
=======
export function greet(name: string): string { // touched
>>>>>>> REPLACE

path: main.go
<<<<<<< SEARCH
func main() {
=======
func main() { // no closing marker follows
EOF
RESP="$(prompt '{"protocol_version":1,"prompt":"edit please"}')"
PROPOSALS="$(printf '%s' "$RESP" | python3 -c 'import json,sys; d=json.loads(sys.stdin.read()); print(len(d.get("edit_proposals") or []))')"
REJECTIONS="$(printf '%s' "$RESP" | python3 -c 'import json,sys; d=json.loads(sys.stdin.read()); print(len(d.get("edit_rejections") or []))')"
say "  edit_proposals=$PROPOSALS edit_rejections=$REJECTIONS"
say "  $(printf '%s' "$RESP" | python3 -c 'import json,sys; print(json.dumps(json.loads(sys.stdin.read()).get("edit_rejections")))')"
[ "$PROPOSALS" = "1" ] && ok "the good block still reaches the client" || bad "edit_proposals=$PROPOSALS, expected 1"
[ "$REJECTIONS" = "1" ] && ok "the malformed block is reported instead of logged and dropped" || bad "edit_rejections=$REJECTIONS, expected 1"

say ""
say "=== 4. EVERY block malformed — the case that used to send nothing at all ==="
cat > "$REPLY" <<'EOF'
path: main.go
<<<<<<< SEARCH
func main() {
=======
func main() { // still no closing marker
EOF
RESP="$(prompt '{"protocol_version":1,"prompt":"edit please"}')"
PROPOSALS="$(printf '%s' "$RESP" | python3 -c 'import json,sys; d=json.loads(sys.stdin.read()); print(len(d.get("edit_proposals") or []))')"
REJECTIONS="$(printf '%s' "$RESP" | python3 -c 'import json,sys; d=json.loads(sys.stdin.read()); print(len(d.get("edit_rejections") or []))')"
say "  edit_proposals=$PROPOSALS edit_rejections=$REJECTIONS"
[ "$PROPOSALS" = "0" ] && ok "no proposals, correctly" || bad "edit_proposals=$PROPOSALS, expected 0"
if [ "$REJECTIONS" -ge 1 ]; then
  ok "the user is told WHY nothing happened — this is what a9c0a2a fixed"
else
  bad "edit_rejections=$REJECTIONS. A reply full of broken edit blocks produced silence."
fi

say ""
say "wire-drill: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
