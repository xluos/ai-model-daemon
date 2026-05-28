"""RapidOCR HTTP server for ai-model-daemon."""
import argparse, json, tempfile, os
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs

import numpy as np

try:
    import cv2
except Exception:
    cv2 = None

from rapidocr import RapidOCR

try:
    from rapidocr import EngineType, LangDet, LangRec, ModelType, OCRVersion
except Exception:
    EngineType = LangDet = LangRec = ModelType = OCRVersion = None

parser = argparse.ArgumentParser()
parser.add_argument("--host", default="127.0.0.1")
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--model", default="rapidocr-ppocr-v5-mobile")
args = parser.parse_args()


def enum_value(enum_cls, name, fallback):
    if enum_cls is None:
        return fallback
    return getattr(enum_cls, name, fallback)


def model_type_for(model_id):
    if "server" in model_id:
        return enum_value(ModelType, "SERVER", "server")
    return enum_value(ModelType, "MOBILE", "mobile")


def base_params(model_id):
    model_type = model_type_for(model_id)
    return {
        "Global.use_cls": False,
        "Det.engine_type": enum_value(EngineType, "ONNXRUNTIME", "onnxruntime"),
        "Det.lang_type": enum_value(LangDet, "CH", "ch"),
        "Det.model_type": model_type,
        "Det.ocr_version": enum_value(OCRVersion, "PPOCRV5", "PP-OCRv5"),
        "Rec.engine_type": enum_value(EngineType, "ONNXRUNTIME", "onnxruntime"),
        "Rec.lang_type": enum_value(LangRec, "CH", "ch"),
        "Rec.model_type": model_type,
        "Rec.ocr_version": enum_value(OCRVersion, "PPOCRV5", "PP-OCRv5"),
        "Cls.engine_type": enum_value(EngineType, "ONNXRUNTIME", "onnxruntime"),
        "Cls.lang_type": enum_value(LangDet, "CH", "ch"),
        "Cls.model_type": enum_value(ModelType, "MOBILE", "mobile"),
        "Cls.ocr_version": enum_value(OCRVersion, "PPOCRV4", "PP-OCRv4"),
    }


def build_engine(model_id, overrides=None):
    params = base_params(model_id)
    if overrides:
        params.update(overrides)
    return RapidOCR(params=params)


engine_cache = {}


def get_engine(overrides=None):
    key = tuple(sorted((overrides or {}).items()))
    engine = engine_cache.get(key)
    if engine is None:
        engine = build_engine(args.model, overrides)
        engine_cache[key] = engine
    return engine


ocr = get_engine()
print(f"RapidOCR server ready on {args.host}:{args.port} model={args.model}", flush=True)

INPUT_INT_PARAMS = ("max_side", "resize_long_side")


def first(qs, name):
    vals = qs.get(name)
    if not vals:
        return None
    value = vals[0].strip()
    return value if value != "" else None


def parse_float(qs, name):
    raw = first(qs, name)
    if raw is None:
        return None
    try:
        return float(raw)
    except ValueError:
        return None


def parse_int(qs, name):
    raw = first(qs, name)
    if raw is None:
        return None
    try:
        return int(raw)
    except ValueError:
        return None


def parse_bool(qs, name):
    raw = first(qs, name)
    if raw is None:
        return None
    lowered = raw.lower()
    if lowered in ("1", "true", "yes", "on"):
        return True
    if lowered in ("0", "false", "no", "off"):
        return False
    return None


def parse_engine_overrides(qs):
    params = {}
    if (value := parse_float(qs, "det_thresh")) is not None:
        params["Det.thresh"] = value
    if (value := parse_int(qs, "det_limit_side_len")) is not None:
        params["Det.limit_side_len"] = value
    if (value := first(qs, "det_limit_type")) is not None:
        params["Det.limit_type"] = value
    if (value := parse_bool(qs, "use_cls")) is not None:
        params["Global.use_cls"] = value
    if (value := parse_int(qs, "min_height")) is not None:
        params["Global.min_height"] = value
    if (value := parse_int(qs, "max_side_len")) is not None:
        params["Global.max_side_len"] = value
    if (value := parse_int(qs, "min_side_len")) is not None:
        params["Global.min_side_len"] = value
    return params


def parse_call_params(qs):
    params = {
        "use_det": True,
        "use_cls": parse_bool(qs, "use_cls") or False,
        "use_rec": True,
    }
    if (value := parse_bool(qs, "use_det")) is not None:
        params["use_det"] = value
    if (value := parse_bool(qs, "use_rec")) is not None:
        params["use_rec"] = value
    if (value := parse_bool(qs, "return_word_box")) is not None:
        params["return_word_box"] = value
    if (value := parse_bool(qs, "return_single_char_box")) is not None:
        params["return_single_char_box"] = value
    if (value := parse_float(qs, "rec_thresh")) is not None:
        params["text_score"] = value
    if (value := parse_float(qs, "text_score")) is not None:
        params["text_score"] = value
    if (value := parse_float(qs, "box_thresh")) is not None:
        params["box_thresh"] = value
    if (value := parse_float(qs, "unclip_ratio")) is not None:
        params["unclip_ratio"] = value
    return params


def parse_max_side(qs):
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


def prepare_image(data, max_side):
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


def to_list(value):
    if value is None:
        return []
    if hasattr(value, "tolist"):
        return value.tolist()
    return list(value)


def serialize(result):
    boxes = to_list(getattr(result, "boxes", []))
    texts = list(getattr(result, "txts", []) or [])
    scores = list(getattr(result, "scores", []) or [])
    output = []
    for i, text in enumerate(texts):
        box = boxes[i] if i < len(boxes) else []
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
        max_side = parse_max_side(qs)
        engine = get_engine(parse_engine_overrides(qs))
        call_params = parse_call_params(qs)

        length = int(self.headers.get("Content-Length", 0))
        data = self.rfile.read(length)
        data = prepare_image(data, max_side)

        tmp = tempfile.NamedTemporaryFile(suffix=".png", delete=False)
        tmp.write(data)
        tmp.close()
        try:
            result = engine(tmp.name, **call_params)
            body = json.dumps(serialize(result), ensure_ascii=False).encode()
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
