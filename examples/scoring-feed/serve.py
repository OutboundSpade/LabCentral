"""Run inside the hidden scorer VM to expose its public JSON projection on loopback.

The scoring engine must atomically publish completed-round snapshots. This process
does not run checks, calculate scores, read scorer credentials, or refresh timestamps.
"""

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def server(feed: Path, port: int) -> ThreadingHTTPServer:
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path != "/scoreboard.json":
                self.send_error(404)
                return
            try:
                with feed.open("rb") as stream:
                    body = stream.read((128 << 10) + 1)
                if not body or len(body) > 128 << 10:
                    raise ValueError("invalid feed size")
            except (OSError, ValueError):
                self.send_error(503, "No completed scoreboard snapshot available")
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args):
            pass

    return ThreadingHTTPServer(("127.0.0.1", port), Handler)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--file", type=Path, required=True)
    parser.add_argument("--port", type=int, default=18080)
    args = parser.parse_args()
    if not 1024 <= args.port <= 65535:
        parser.error("port must be between 1024 and 65535")
    server(args.file, args.port).serve_forever()
