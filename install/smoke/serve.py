"""Serve a directory of release files on 127.0.0.1, for the install smoke test.

    python3 serve.py <dir> <port>

`python3 -m http.server` would do, but its server looks up the bind address's
host name when it starts, and on GitHub's macOS runners that took about 35
seconds (measured 2026-10-19). This is the same server without the lookup.
Standard library only.
"""

import functools
import socketserver
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer


class QuickServer(ThreadingHTTPServer):
    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
        self.server_name, self.server_port = self.server_address[:2]


def main():
    directory, port = sys.argv[1], int(sys.argv[2])
    handler = functools.partial(SimpleHTTPRequestHandler, directory=directory)
    QuickServer(("127.0.0.1", port), handler).serve_forever()


if __name__ == "__main__":
    main()
