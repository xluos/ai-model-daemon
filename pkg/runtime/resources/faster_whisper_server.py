"""Minimal faster-whisper HTTP server for ai-model-daemon.

OpenAI-compatible /v1/audio/transcriptions endpoint.
"""
import argparse, json, os, sys, tempfile, time
from http.server import HTTPServer, BaseHTTPRequestHandler
from email.parser import BytesParser
from io import BytesIO

parser = argparse.ArgumentParser()
parser.add_argument("--host", default="127.0.0.1")
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--model", required=True, help="Model directory or name")
parser.add_argument("--device", default="auto")
parser.add_argument("--compute-type", default="auto")
parser.add_argument("--threads", type=int, default=4)
args = parser.parse_args()

from faster_whisper import WhisperModel

model = WhisperModel(
    args.model,
    device=args.device,
    compute_type=args.compute_type,
    cpu_threads=args.threads,
)
print(f"faster-whisper server ready on {args.host}:{args.port}", flush=True)


def parse_multipart(body, content_type):
    """Extract file data and form fields from multipart body."""
    headers = f"Content-Type: {content_type}\r\n\r\n".encode()
    msg = BytesParser().parsebytes(headers + body)
    fields = {}
    file_data = None
    file_name = "audio"
    for part in msg.walk():
        cd = part.get("Content-Disposition", "")
        if 'name="file"' in cd:
            file_data = part.get_payload(decode=True)
            for token in cd.split(";"):
                token = token.strip()
                if token.startswith("filename="):
                    file_name = token.split("=", 1)[1].strip('"')
        elif "name=" in cd:
            name = ""
            for token in cd.split(";"):
                token = token.strip()
                if token.startswith("name="):
                    name = token.split("=", 1)[1].strip('"')
            if name:
                fields[name] = part.get_payload(decode=True).decode()
    return file_data, file_name, fields


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        ct = self.headers.get("Content-Type", "")

        if "multipart" in ct:
            file_data, file_name, fields = parse_multipart(body, ct)
        else:
            file_data = body
            file_name = "audio.wav"
            fields = {}

        if not file_data:
            self._error(400, "no audio file provided")
            return

        ext = os.path.splitext(file_name)[1] or ".wav"
        tmp = tempfile.NamedTemporaryFile(suffix=ext, delete=False)
        tmp.write(file_data)
        tmp.close()

        try:
            language = fields.get("language")
            segments, info = model.transcribe(
                tmp.name,
                language=language if language else None,
                beam_size=5,
            )
            text_parts = [seg.text for seg in segments]
            full_text = "".join(text_parts).strip()

            resp_format = fields.get("response_format", "json")
            if resp_format == "text":
                body_out = full_text.encode()
                ctype = "text/plain"
            else:
                body_out = json.dumps(
                    {"text": full_text, "language": info.language, "duration": info.duration},
                    ensure_ascii=False,
                ).encode()
                ctype = "application/json"

            self.send_response(200)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body_out)))
            self.end_headers()
            self.wfile.write(body_out)
        except Exception as e:
            self._error(500, str(e))
        finally:
            os.unlink(tmp.name)

    def _error(self, code, msg):
        body = json.dumps({"error": {"message": msg, "type": "server_error"}}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *a):
        pass


HTTPServer((args.host, args.port), Handler).serve_forever()
