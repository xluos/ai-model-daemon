"""Minimal PaddleOCR HTTP server for ai-model-daemon."""
import argparse, json, os, sys, tempfile
from http.server import HTTPServer, BaseHTTPRequestHandler

parser = argparse.ArgumentParser()
parser.add_argument("--host", default="127.0.0.1")
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--det", required=True, help="Detection model directory")
parser.add_argument("--rec", required=True, help="Recognition model directory")
parser.add_argument("--cls", default="", help="Classification model directory")
parser.add_argument("--lang", default="ch")
args = parser.parse_args()

from paddleocr import PaddleOCR

ocr = PaddleOCR(
    det_model_dir=args.det,
    rec_model_dir=args.rec,
    cls_model_dir=args.cls or None,
    use_angle_cls=bool(args.cls),
    lang=args.lang,
    show_log=False,
)
print(f"OCR server ready on {args.host}:{args.port}", flush=True)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        data = self.rfile.read(length)
        tmp = tempfile.NamedTemporaryFile(suffix=".png", delete=False)
        tmp.write(data)
        tmp.close()
        try:
            result = ocr.ocr(tmp.name, cls=bool(args.cls))
            output = []
            if result and result[0]:
                for line in result[0]:
                    box, (text, confidence) = line
                    output.append({"box": box, "text": text, "confidence": confidence})
            body = json.dumps(output, ensure_ascii=False).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except Exception as e:
            body = json.dumps({"error": str(e)}).encode()
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        finally:
            os.unlink(tmp.name)

    def log_message(self, format, *a):
        pass


HTTPServer((args.host, args.port), Handler).serve_forever()
