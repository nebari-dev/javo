"""A fake OpenAI-compatible model server for the AI end-to-end test.

It answers /v1/chat/completions with the JSON each javo feature asks for (by
the response format's schema name), citing the first data ref it was sent,
and keeps every request so the test can read what reached the model at
GET /requests. Standard library only; no real model is called.
"""
import http.server
import json
import re

REQUESTS = []


def answer(name, schema, user):
    refs = re.findall(r'ref="([^"]+)"', user)
    if name == "self_test":
        return {"ok": True}
    if name == "explain":
        return {
            "summary": "The app stopped while importing a module that is not installed.",
            "cause": "A missing Python module.",
            "confidence": "medium",
            "evidence": refs[:1],
            "fix": "Install the module, then restart.",
            "actions": [],
        }
    if name == "draft":
        templates = schema.get("properties", {}).get("template", {}).get("enum", [])
        pick = next((t for t in templates if "streamlit" in t), templates[0] if templates else "")
        return {"displayName": "Sales draft", "template": pick, "file": "e2e/hello.py"}
    return {}


class Handler(http.server.BaseHTTPRequestHandler):
    def reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/requests":
            self.reply(200, REQUESTS)
        else:
            self.reply(404, {"error": {"message": "not found"}})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        REQUESTS.append(body)
        fmt = (body.get("response_format") or {}).get("json_schema") or {}
        user = body["messages"][-1]["content"]
        if isinstance(user, list):
            user = "".join(p.get("text", "") for p in user)
        out = answer(fmt.get("name", ""), fmt.get("schema") or {}, user)
        self.reply(200, {
            "id": "fake-1", "object": "chat.completion", "created": 0, "model": "fake-model",
            "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": json.dumps(out)}}],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
        })

    def log_message(self, *args):
        pass


http.server.ThreadingHTTPServer(("", 8000), Handler).serve_forever()
