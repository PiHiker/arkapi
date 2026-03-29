import json
import os
import shutil
import subprocess
import tempfile
import time
from http.server import BaseHTTPRequestHandler, HTTPServer


PORT = int(os.environ.get("PORT", "9021"))
TOKEN = os.environ.get("ARKAPI_HASH_CRACK_SERVICE_TOKEN", "change-me-hash-crack-token")
WORDLIST = "/app/fasttrack.txt"
MAX_BODY = 16 * 1024
JOHN_BIN = "/opt/john/run/john"
JOHN_CWD = "/opt/john/run"


def json_response(handler, status, payload):
    data = json.dumps(payload).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(data)))
    handler.send_header("Cache-Control", "no-store")
    handler.end_headers()
    handler.wfile.write(data)


def parse_show_output(output):
    for line in output.splitlines():
        line = line.strip()
        if not line or "password hash cracked" in line:
            continue
        if ":" not in line:
            continue
        _, plaintext = line.split(":", 1)
        plaintext = plaintext.strip()
        if plaintext:
            return plaintext
    return ""


def crack_hash(hash_value, john_format, max_seconds):
    started = time.time()
    workdir = tempfile.mkdtemp(prefix="arkapi-john-")
    try:
        hashfile = os.path.join(workdir, "hash.txt")
        potfile = os.path.join(workdir, "john.pot")

        with open(hashfile, "w", encoding="utf-8") as fh:
            fh.write(hash_value + "\n")

        home_dir = os.path.join(workdir, "home")
        os.makedirs(os.path.join(home_dir, ".john"), exist_ok=True)
        common_env = dict(os.environ)
        common_env["HOME"] = home_dir

        john_cmd = [
            JOHN_BIN,
            f"--format={john_format}",
            f"--wordlist={WORDLIST}",
            f"--pot={potfile}",
            "--rules=Wordlist",
            hashfile,
        ]

        timed_out = False
        try:
            subprocess.run(
                john_cmd,
                cwd=JOHN_CWD,
                env=common_env,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                timeout=max_seconds,
                check=False,
            )
        except subprocess.TimeoutExpired:
            timed_out = True

        show_cmd = [
            JOHN_BIN,
            "--show",
            f"--format={john_format}",
            f"--pot={potfile}",
            hashfile,
        ]
        show = subprocess.run(
            show_cmd,
            cwd=JOHN_CWD,
            env=common_env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=5,
            check=False,
        )
        plaintext = parse_show_output(show.stdout)
        return {
            "cracked": bool(plaintext),
            "plaintext": plaintext,
            "timed_out": timed_out and not plaintext,
            "elapsed_ms": int((time.time() - started) * 1000),
        }
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/crack":
            json_response(self, 404, {"error": "not found"})
            return

        if self.headers.get("X-Hash-Crack-Token") != TOKEN:
            json_response(self, 403, {"error": "forbidden"})
            return

        length = int(self.headers.get("Content-Length", "0") or "0")
        if length <= 0 or length > MAX_BODY:
            json_response(self, 400, {"error": "invalid request body"})
            return

        raw = self.rfile.read(length)
        try:
            payload = json.loads(raw.decode("utf-8"))
        except Exception:
            json_response(self, 400, {"error": "invalid json"})
            return

        hash_value = str(payload.get("hash", "")).strip().lower()
        john_format = str(payload.get("format", "")).strip()
        hash_type = str(payload.get("type", "")).strip().lower()
        mode = str(payload.get("mode", "fasttrack")).strip().lower() or "fasttrack"
        max_seconds = int(payload.get("max_seconds", 10) or 10)

        if not hash_value or not john_format or not hash_type:
            json_response(self, 400, {"error": "hash, type, and format are required"})
            return
        if mode != "fasttrack":
            json_response(self, 400, {"error": "unsupported mode"})
            return
        if max_seconds < 1:
            max_seconds = 1
        if max_seconds > 15:
            max_seconds = 15

        result = crack_hash(hash_value, john_format, max_seconds)
        json_response(
            self,
            200,
            {
                "hash": hash_value,
                "type": hash_type,
                "mode": mode,
                "engine": "john",
                "ruleset": "fasttrack.txt+wordlist-rules",
                "cracked": result["cracked"],
                "plaintext": result["plaintext"],
                "timed_out": result["timed_out"],
                "elapsed_ms": result["elapsed_ms"],
            },
        )

    def log_message(self, fmt, *args):
        return


if __name__ == "__main__":
    server = HTTPServer(("0.0.0.0", PORT), Handler)
    server.serve_forever()
