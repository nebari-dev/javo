"""A custom app: a standard-library web server that shows the server time.

Started as: python sample.py --port <port> --base-url <prefix>
"""
import argparse
import html
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--base-url", required=True)
args = parser.parse_args()
base = args.base_url if args.base_url.endswith("/") else args.base_url + "/"


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.split("?")[0] != base:
            self.send_error(404)
            return
        now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
        body = (
            "<!doctype html><meta charset=utf-8><title>Server time</title>"
            f"<h1>Server time</h1><p>{html.escape(now)}</p>"
            "<p>Edit sample.py and restart the app to change this page.</p>"
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


ThreadingHTTPServer(("0.0.0.0", args.port), Handler).serve_forever()
