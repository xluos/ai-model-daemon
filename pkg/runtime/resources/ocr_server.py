"""Minimal PaddleOCR HTTP server for ai-model-daemon."""
import argparse, json, os, sys, tempfile
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs

parser = argparse.ArgumentParser()
parser.add_argument("--host", default="127.0.0.1")
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--det", default="", help="Detection model directory")
parser.add_argument("--rec", default="", help="Recognition model directory")
parser.add_argument("--cls", default="", help="Classification model directory")
parser.add_argument("--lang", default="ch")
args = parser.parse_args()

os.environ.setdefault("PADDLE_PDX_DISABLE_MODEL_SOURCE_CHECK", "True")

import yaml
from paddleocr import PaddleOCR
import numpy as np

try:
    import cv2
except Exception:
    cv2 = None

def read_model_name(model_dir):
    yml = os.path.join(model_dir, "inference.yml")
    if os.path.exists(yml):
        with open(yml) as f:
            cfg = yaml.safe_load(f)
        return cfg.get("Global", {}).get("model_name")
    return None

kwargs = dict(
    use_doc_orientation_classify=False,
    use_doc_unwarping=False,
    use_textline_orientation=bool(args.cls),
    lang=args.lang,
)
if args.det:
    kwargs["text_detection_model_dir"] = args.det
    name = read_model_name(args.det)
    if name:
        kwargs["text_detection_model_name"] = name
if args.rec:
    kwargs["text_recognition_model_dir"] = args.rec
    name = read_model_name(args.rec)
    if name:
        kwargs["text_recognition_model_name"] = name
if args.cls:
    kwargs["textline_orientation_model_dir"] = args.cls

ocr = PaddleOCR(**kwargs)
print(f"OCR server ready on {args.host}:{args.port}", flush=True)

PREDICT_FLOAT_PARAMS = {
    "det_thresh": "text_det_thresh",
    "box_thresh": "text_det_box_thresh",
    "unclip_ratio": "text_det_unclip_ratio",
    "rec_thresh": "text_rec_score_thresh",
}
PREDICT_INT_PARAMS = {
    "det_limit_side_len": "text_det_limit_side_len",
}
INPUT_INT_PARAMS = ("max_side", "resize_long_side")


def _parse_predict_params(qs):
    params = {}
    for qname, pname in PREDICT_FLOAT_PARAMS.items():
        vals = qs.get(qname)
        if vals:
            try:
                params[pname] = float(vals[0])
            except ValueError:
                pass
    for qname, pname in PREDICT_INT_PARAMS.items():
        vals = qs.get(qname)
        if vals:
            try:
                params[pname] = int(vals[0])
            except ValueError:
                pass
    return params


def _parse_max_side(qs):
    for qname in INPUT_INT_PARAMS:
        vals = qs.get(qname)
        if not vals:
            continue
        try:
            value = int(vals[0])
        except ValueError:
            continue
        if value > 0:
            return value
    return 0


def _prepare_image(data, max_side):
    if not max_side or cv2 is None:
        return data

    arr = np.frombuffer(data, dtype=np.uint8)
    img = cv2.imdecode(arr, cv2.IMREAD_COLOR)
    if img is None:
        return data

    h, w = img.shape[:2]
    longest = max(h, w)
    if longest <= max_side:
        return data

    scale = max_side / float(longest)
    resized = cv2.resize(
        img,
        (max(1, int(round(w * scale))), max(1, int(round(h * scale)))),
        interpolation=cv2.INTER_AREA,
    )
    ok, encoded = cv2.imencode(".png", resized)
    if not ok:
        return data
    return encoded.tobytes()


def _serialize(result):
    output = []
    for r in result:
        texts = r.get("rec_texts", [])
        scores = r.get("rec_scores", [])
        polys = r.get("dt_polys", [])
        for i, text in enumerate(texts):
            box = polys[i].tolist() if i < len(polys) and hasattr(polys[i], "tolist") else []
            score = float(scores[i]) if i < len(scores) else 0.0
            output.append({"box": box, "text": text, "confidence": score})
    return output


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()

    def do_POST(self):
        parsed = urlparse(self.path)
        qs = parse_qs(parsed.query)
        predict_params = _parse_predict_params(qs)
        max_side = _parse_max_side(qs)

        length = int(self.headers.get("Content-Length", 0))
        data = self.rfile.read(length)
        data = _prepare_image(data, max_side)
        tmp = tempfile.NamedTemporaryFile(suffix=".png", delete=False)
        tmp.write(data)
        tmp.close()
        try:
            result = list(ocr.predict(tmp.name, **predict_params))
            output = _serialize(result)
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
