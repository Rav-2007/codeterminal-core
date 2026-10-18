"""A stand-in AI provider for the install smoke test.

It speaks just enough of the OpenAI-compatible chat API for Mochiii to finish
one answer: every POST to .../chat/completions gets a short streamed reply
containing SMOKE-ANSWER-OK. Each request body is appended, one JSON document
per line, to the file named by the second argument, so the test can check what
Mochiii actually sent -- in particular that the fixture project's code reached
the prompt, which proves retrieval worked end to end.

    python3 stub_provider.py <port> <requests-file>

Standard library only: it runs on every CI runner as it is.
"""

import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ANSWER = "SMOKE-ANSWER-OK: the burrow survey is folded into one number by quokkaChecksum."


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        sys.stderr.write("stub: " + (fmt % args) + "\n")

    def do_GET(self):
        if self.path.rstrip("/").endswith("/models"):
            self._json(200, {"object": "list", "data": [{"id": "smoke-model", "object": "model"}]})
            return
        self._json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length)
        with open(self.server.requests_file, "ab") as f:
            f.write(body.replace(b"\n", b" ") + b"\n")
        if not self.path.rstrip("/").endswith("/chat/completions"):
            self._json(404, {"error": {"message": "not found"}})
            return
        try:
            stream = json.loads(body or b"{}").get("stream", False)
        except ValueError:
            stream = False
        if not stream:
            self._json(200, {
                "id": "smoke", "object": "chat.completion", "model": "smoke-model",
                "choices": [{"index": 0, "message": {"role": "assistant", "content": ANSWER},
                             "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
            })
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        chunks = [
            {"choices": [{"index": 0, "delta": {"role": "assistant", "content": ANSWER}, "finish_reason": None}]},
            {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
             "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}},
        ]
        for c in chunks:
            c.update({"id": "smoke", "object": "chat.completion.chunk", "model": "smoke-model"})
            self.wfile.write(b"data: " + json.dumps(c).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def _json(self, code, obj):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def main():
    port, requests_file = int(sys.argv[1]), sys.argv[2]
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.requests_file = requests_file
    server.serve_forever()


if __name__ == "__main__":
    main()
