#!/usr/bin/env python
"""Docling local document extraction for Invoice Tidy."""

from __future__ import annotations

import argparse
import json
import os
import re
import struct
import tempfile
import time
import zlib
from pathlib import Path
from typing import Any

_PYTHON_THREADS = os.environ.get("INVOICE_TIDY_PYTHON_THREADS", "1")
for _thread_env in (
    "OPENBLAS_NUM_THREADS",
    "OMP_NUM_THREADS",
    "MKL_NUM_THREADS",
    "VECLIB_MAXIMUM_THREADS",
    "NUMEXPR_NUM_THREADS",
    "NUMEXPR_MAX_THREADS",
):
    os.environ.setdefault(_thread_env, _PYTHON_THREADS)
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

GRANITE_REPO_ID = "ibm-granite/granite-docling-258M"
GRANITE_CACHE_NAME = "ibm-granite--granite-docling-258M"

_CONVERTERS: dict[tuple[str, str, str], Any] = {}
IMAGE_INPUT_EXTENSIONS = {".png", ".jpg", ".jpeg", ".webp", ".bmp", ".tif", ".tiff"}

DEFAULT_TEMPLATE_FIELDS = [
    {"key": "vendor_name", "label": "Vendor", "type": "text", "required": True, "hint": "Vendor or supplier name"},
    {"key": "invoice_number", "label": "Invoice #", "type": "text", "required": True, "hint": "Invoice number"},
    {"key": "invoice_date", "label": "Invoice date", "type": "date", "required": False, "hint": "Invoice issue date"},
    {"key": "due_date", "label": "Due date", "type": "date", "required": False, "hint": "Payment due date"},
    {"key": "currency", "label": "Currency", "type": "text", "required": False, "hint": "Currency code or symbol"},
    {"key": "subtotal", "label": "Subtotal", "type": "money", "required": False, "hint": "Subtotal before tax"},
    {"key": "tax", "label": "Tax", "type": "money", "required": False, "hint": "Tax amount"},
    {"key": "total", "label": "Total", "type": "money", "required": True, "hint": "Final invoice total"},
]


def _repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def _max_image_side() -> int:
    try:
        return max(800, int(os.environ.get("INVOICE_TIDY_MAX_IMAGE_SIDE", "1400")))
    except ValueError:
        return 1400


def _prepare_low_memory_source(input_path: Path) -> tuple[Path, Path | None]:
    if input_path.suffix.lower() not in IMAGE_INPUT_EXTENSIONS or not input_path.exists():
        return input_path, None

    try:
        from PIL import Image
    except Exception:
        return input_path, None

    max_side = _max_image_side()
    with Image.open(input_path) as image:
        width, height = image.size
        if max(width, height) <= max_side and image.mode == "RGB":
            return input_path, None

        scale = min(1.0, max_side / float(max(width, height)))
        target = (max(1, int(width * scale)), max(1, int(height * scale)))
        normalized = image.convert("RGB")
        if target != image.size:
            normalized = normalized.resize(target, Image.Resampling.LANCZOS)

        handle = tempfile.NamedTemporaryFile(prefix="invoice-tidy-docling-", suffix=".png", delete=False)
        temp_path = Path(handle.name)
        handle.close()
        normalized.save(temp_path, format="PNG", optimize=True)
        return temp_path, temp_path


def _local_paths() -> dict[str, Path]:
    root = _repo_root()
    return {
        "hf_cache": root / "local-tools" / "hf-cache",
        "docling_models": root / "local-tools" / "docling-models",
        "granite_model": root / "local-tools" / "docling-models" / GRANITE_CACHE_NAME,
    }


def ensure_local_env() -> None:
    paths = _local_paths()
    for path in paths.values():
        path.mkdir(parents=True, exist_ok=True)
    os.environ.setdefault("HF_HOME", str(paths["hf_cache"]))
    os.environ.setdefault("TRANSFORMERS_CACHE", str(paths["hf_cache"]))
    os.environ.setdefault("HF_HUB_DISABLE_SYMLINKS_WARNING", "1")


def ensure_granite_model_local(download: bool = True) -> Path | None:
    ensure_local_env()
    target = _local_paths()["granite_model"]
    if (target / "config.json").exists():
        return target
    if not download:
        return None
    from huggingface_hub import snapshot_download

    workers = _int_env("INVOICE_TIDY_HF_DOWNLOAD_WORKERS", 2)
    snapshot_download(
        repo_id=GRANITE_REPO_ID,
        revision="main",
        local_dir=str(target),
        max_workers=max(1, workers),
    )
    return target if (target / "config.json").exists() else None


def _write_warmup_png(path: Path, width: int = 320, height: int = 220) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists():
        return

    def chunk(kind: bytes, data: bytes) -> bytes:
        return (
            struct.pack(">I", len(data))
            + kind
            + data
            + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
        )

    rows = b"".join(b"\x00" + (b"\xff\xff\xff" * width) for _ in range(height))
    payload = b"\x89PNG\r\n\x1a\n"
    payload += chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
    payload += chunk(b"IDAT", zlib.compress(rows, 6))
    payload += chunk(b"IEND", b"")
    path.write_bytes(payload)


def warm_docling_converter(mode: str = "standard", device: str = "auto") -> None:
    ensure_local_env()
    selected_device = _requested_device(device)
    selected_mode = "light" if mode == "light" else "standard"
    kind = "layout" if selected_mode == "light" or os.getenv("INVOICE_TIDY_DOCLING_LAYOUT_ONLY", "0") == "1" else "granite"
    converter = _converter(kind, selected_mode, selected_device)

    from docling.datamodel.base_models import InputFormat

    converter.initialize_pipeline(InputFormat.PDF)
    converter.initialize_pipeline(InputFormat.IMAGE)


def _int_env(name: str, default: int) -> int:
    try:
        return int(os.getenv(name, str(default)))
    except Exception:
        return default


def _float_env(name: str, default: float) -> float:
    try:
        return float(os.getenv(name, str(default)))
    except Exception:
        return default


def _requested_device(requested: str) -> str:
    normalized = (requested or "auto").lower()
    if normalized in {"cpu"}:
        return "cpu"
    if normalized in {"gpu", "gpu:0", "cuda"}:
        return "cuda"
    try:
        import torch

        if torch.cuda.is_available():
            return "cuda"
    except Exception:
        pass
    return "cpu"


def _torch_dtype(device: str) -> Any | None:
    dtype_name = os.getenv("INVOICE_TIDY_DOCLING_DTYPE", "float16" if device == "cuda" else "auto").lower()
    if dtype_name in {"", "auto", "none"}:
        return None
    import torch

    if dtype_name in {"fp16", "float16", "half"}:
        return torch.float16
    if dtype_name in {"bf16", "bfloat16"}:
        return torch.bfloat16
    return None


def _make_granite_converter(mode: str, device: str) -> Any:
    ensure_local_env()
    ensure_granite_model_local(download=os.getenv("INVOICE_TIDY_GRANITE_AUTO_DOWNLOAD", "1") != "0")

    from docling.datamodel.base_models import InputFormat
    from docling.datamodel.pipeline_options import AcceleratorOptions, VlmPipelineOptions
    from docling.document_converter import DocumentConverter, ImageFormatOption, PdfFormatOption
    from docling.models.inference_engines.vlm.base import VlmEngineType
    from docling.pipeline.vlm_pipeline import VlmPipeline

    selected_mode = "light" if mode == "light" else "standard"
    constrained_standard = selected_mode == "standard"
    scale_default = 1.0 if selected_mode == "light" or constrained_standard else 1.5
    max_size_default = 1280 if selected_mode == "light" else (1024 if constrained_standard else 1536)

    options = VlmPipelineOptions()
    options.accelerator_options = AcceleratorOptions(
        device=device,
        num_threads=_int_env("INVOICE_TIDY_DOCLING_THREADS", 4),
        cuda_use_flash_attention2=os.getenv("INVOICE_TIDY_DOCLING_FLASH_ATTN2", "0") == "1",
    )
    options.artifacts_path = str(_local_paths()["docling_models"])
    timeout_default = 420 if constrained_standard else 900
    options.document_timeout = _int_env("INVOICE_TIDY_DOCLING_DOCUMENT_TIMEOUT", timeout_default)
    options.images_scale = 1.0
    options.generate_page_images = True
    options.generate_picture_images = False
    options.vlm_options.scale = _float_env("INVOICE_TIDY_DOCLING_SCALE", scale_default)
    options.vlm_options.max_size = _int_env("INVOICE_TIDY_DOCLING_MAX_SIZE", max_size_default)
    options.vlm_options.batch_size = 1
    options.vlm_options.model_spec.default_repo_id = GRANITE_REPO_ID
    token_default = 1024 if selected_mode == "light" else (768 if constrained_standard else 2048)
    options.vlm_options.model_spec.max_new_tokens = _int_env("INVOICE_TIDY_DOCLING_MAX_NEW_TOKENS", token_default)

    override = options.vlm_options.model_spec.engine_overrides.get(VlmEngineType.TRANSFORMERS)
    if override is not None:
        dtype = _torch_dtype(device)
        if dtype is not None:
            override.extra_config["torch_dtype"] = dtype
        generation = dict(override.extra_config.get("extra_generation_config") or {})
        generation.setdefault("skip_special_tokens", False)
        generation.setdefault("do_sample", False)
        override.extra_config["extra_generation_config"] = generation

    return DocumentConverter(
        allowed_formats=[InputFormat.PDF, InputFormat.IMAGE],
        format_options={
            InputFormat.PDF: PdfFormatOption(pipeline_cls=VlmPipeline, pipeline_options=options),
            InputFormat.IMAGE: ImageFormatOption(pipeline_cls=VlmPipeline, pipeline_options=options),
        },
    )


def _make_layout_converter(mode: str, device: str) -> Any:
    ensure_local_env()

    from docling.datamodel.base_models import InputFormat
    from docling.datamodel.pipeline_options import AcceleratorOptions, PdfPipelineOptions, RapidOcrOptions
    from docling.datamodel.settings import settings
    from docling.document_converter import DocumentConverter, ImageFormatOption, PdfFormatOption

    os.environ.pop("DOCLING_ARTIFACTS_PATH", None)
    settings.artifacts_path = None
    selected_mode = "light" if mode == "light" else "standard"
    options = PdfPipelineOptions()
    options.artifacts_path = None
    options.accelerator_options = AcceleratorOptions(
        device=device,
        num_threads=_int_env("INVOICE_TIDY_DOCLING_THREADS", 4),
    )
    options.do_ocr = True
    options.do_table_structure = True
    options.generate_page_images = False
    options.generate_picture_images = False
    options.ocr_batch_size = 1 if selected_mode == "light" else 4
    options.layout_batch_size = 1 if selected_mode == "light" else 4
    options.table_batch_size = 1 if selected_mode == "light" else 4
    options.ocr_options = RapidOcrOptions(
        lang=["english"],
        backend="onnxruntime",
        force_full_page_ocr=True,
        text_score=0.25,
    )
    return DocumentConverter(
        allowed_formats=[InputFormat.PDF, InputFormat.IMAGE],
        format_options={
            InputFormat.PDF: PdfFormatOption(pipeline_options=options),
            InputFormat.IMAGE: ImageFormatOption(pipeline_options=options),
        },
    )


def _converter(kind: str, mode: str, device: str) -> Any:
    actual_device = _requested_device(device)
    key = (kind, "light" if mode == "light" else "standard", actual_device)
    if key not in _CONVERTERS:
        maker = _make_granite_converter if kind == "granite" else _make_layout_converter
        _CONVERTERS[key] = maker(key[1], actual_device)
    return _CONVERTERS[key]


def _json_default(value: Any) -> Any:
    try:
        import torch

        if isinstance(value, torch.dtype):
            return str(value).replace("torch.", "")
    except Exception:
        pass
    if hasattr(value, "model_dump"):
        return value.model_dump()
    if hasattr(value, "dict"):
        return value.dict()
    if isinstance(value, Path):
        return str(value)
    return str(value)


def _as_dict(doc: Any) -> dict[str, Any]:
    try:
        return doc.export_to_dict()
    except Exception:
        return {}


def _rect_from_prov(prov: Any) -> dict[str, float] | None:
    if not isinstance(prov, list) or not prov:
        return None
    item = prov[0] if isinstance(prov[0], dict) else {}
    bbox = item.get("bbox")
    if not isinstance(bbox, dict):
        return None
    try:
        left = float(bbox.get("l", bbox.get("left", bbox.get("x", 0))))
        top = float(bbox.get("t", bbox.get("top", bbox.get("y", 0))))
        right = float(bbox.get("r", bbox.get("right", left)))
        bottom = float(bbox.get("b", bbox.get("bottom", top)))
        return {
            "x": min(left, right),
            "y": min(top, bottom),
            "width": abs(right - left),
            "height": abs(bottom - top),
            "coordOrigin": bbox.get("coord_origin") or bbox.get("coordOrigin"),
        }
    except Exception:
        return None


def _page_from_prov(prov: Any) -> int:
    if isinstance(prov, list) and prov and isinstance(prov[0], dict):
        try:
            return int(prov[0].get("page_no") or prov[0].get("page") or 1)
        except Exception:
            return 1
    return 1


def _page_size_map(payload: dict[str, Any]) -> dict[int, dict[str, Any]]:
    sizes: dict[int, dict[str, Any]] = {}
    page_map = payload.get("pages") or {}
    if not isinstance(page_map, dict):
        return sizes
    for page_no, page in page_map.items():
        if not isinstance(page, dict):
            continue
        size = page.get("size") or {}
        try:
            key = int(page.get("page_no") or page_no)
        except Exception:
            continue
        sizes[key] = {
            "width": size.get("width"),
            "height": size.get("height"),
        }
    return sizes


def _collect_blocks(payload: dict[str, Any], text: str) -> list[dict[str, Any]]:
    blocks: list[dict[str, Any]] = []
    page_sizes = _page_size_map(payload)
    for index, item in enumerate(payload.get("texts") or []):
        if not isinstance(item, dict):
            continue
        value = str(item.get("text") or item.get("orig") or "").strip()
        if not value:
            continue
        page_no = _page_from_prov(item.get("prov"))
        size = page_sizes.get(page_no) or {}
        blocks.append(
            {
                "id": item.get("self_ref") or f"block-{index + 1}",
                "page": page_no,
                "pageWidth": size.get("width"),
                "pageHeight": size.get("height"),
                "type": str(item.get("label") or "text"),
                "text": value,
                "confidence": None,
                "box": _rect_from_prov(item.get("prov")),
                "raw": item,
                "readingOrder": index,
            }
        )
    seen_text = "\n".join(str(block.get("text") or "").strip().lower() for block in blocks)
    line_offset = len(blocks)
    for index, line in enumerate([part.strip() for part in text.splitlines() if part.strip()]):
        normalized = re.sub(r"\s+", " ", line).strip().lower()
        if normalized and normalized in seen_text:
            continue
        blocks.append(
            {
                "id": f"text-line-{index + 1}",
                "page": 1,
                "type": "text",
                "text": line,
                "confidence": None,
                "box": None,
                "readingOrder": line_offset + index,
            }
        )
    return blocks


def _collect_tables(payload: dict[str, Any]) -> list[dict[str, Any]]:
    tables: list[dict[str, Any]] = []
    for index, item in enumerate(payload.get("tables") or []):
        if not isinstance(item, dict):
            continue
        tables.append(
            {
                "id": item.get("self_ref") or f"table-{index + 1}",
                "page": _page_from_prov(item.get("prov")),
                "box": _rect_from_prov(item.get("prov")),
                "data": item,
            }
        )
    return tables


def _pages(payload: dict[str, Any]) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for page_no, size in _page_size_map(payload).items():
        rows.append(
            {
                "page": page_no,
                "width": size.get("width"),
                "height": size.get("height"),
            }
        )
    return rows or [{"page": 1, "width": None, "height": None}]


def _first_match(patterns: list[str], text: str) -> str:
    for pattern in patterns:
        match = re.search(pattern, text, re.IGNORECASE)
        if match:
            return (match.group(1) if match.groups() else match.group(0)).strip()
    return ""


MONEY_RE = r"[\$\u20ac\u00a3]?\s?\d{1,3}(?:[,\s]\d{3})*(?:[\.,]\d{2})?|\d+(?:[\.,]\d{2})"
DATE_RE = r"\d{4}[-/]\d{1,2}[-/]\d{1,2}|\d{1,2}[-/\.]\d{1,2}[-/\.]\d{2,4}|[A-Z][a-z]{2,9}\s*\d{1,2},?\s*\d{4}"

FIELD_ALIASES: dict[str, list[str]] = {
    "vendor_name": ["vendor", "supplier", "merchant", "seller", "billed by", "from", "remit to", "company"],
    "invoice_number": [
        "invoice number",
        "invoice no",
        "invoice no.",
        "invoice #",
        "invoice id",
        "bill number",
        "receipt number",
        "document number",
    ],
    "invoice_date": ["invoice date", "issue date", "issued date", "bill date", "date"],
    "due_date": ["due date", "payment due date", "date payment due", "payable by", "due by", "total due by"],
    "currency": ["currency", "currency code", "currency symbol"],
    "subtotal": ["subtotal", "sub total", "sum", "net amount", "amount before tax", "total before tax"],
    "tax": ["tax", "tax amount", "sales tax", "vat", "gst", "turnover tax", "ustg"],
    "total": ["total", "grand total", "invoice total", "amount due", "balance due", "total due", "total price"],
}


def _normalize_money(value: str) -> str:
    return re.sub(r"\s+", "", str(value or ""))


def _normalize_date(value: str) -> str:
    value = _clean_cell(value)
    match = re.fullmatch(r"([A-Z][a-z]{2,9})\s*(\d{1,2}),?\s*(\d{4})", value, re.IGNORECASE)
    if match:
        month = match.group(1).capitalize()
        return f"{month} {int(match.group(2))}, {match.group(3)}"
    return value


def _last_money(text: str) -> str:
    matches = re.findall(MONEY_RE, text)
    return matches[-1].strip() if matches else ""


def _normalize_label(value: str) -> str:
    return re.sub(r"[^a-z0-9]+", "", str(value or "").lower())


def _clean_cell(value: str) -> str:
    return re.sub(r"\s+", " ", str(value or "").strip().strip("|")).strip()


def _split_table_row(line: str) -> list[str]:
    if "|" not in line:
        return []
    return [_clean_cell(cell) for cell in line.strip().strip("|").split("|")]


def _label_matches(normalized_label: str, normalized_candidate: str) -> bool:
    if not normalized_label:
        return False
    if normalized_label == "total":
        return normalized_candidate == "total" or (
            normalized_candidate.startswith("total") and not normalized_candidate.startswith("subtotal")
        )
    if normalized_label == "tax":
        return normalized_candidate == "tax" or normalized_candidate.startswith("tax")
    return normalized_label in normalized_candidate


def _valid_value(value: str, kind: str) -> str:
    value = _clean_cell(value)
    if not value or re.fullmatch(r"[-:| ]+", value):
        return ""
    if kind == "money":
        return _normalize_money(_last_money(value))
    if kind == "date":
        match = re.search(DATE_RE, value, re.IGNORECASE)
        return _normalize_date(match.group(0).strip()) if match else ""
    if kind == "id":
        candidate = value.strip()
        candidate = re.sub(r"^(?:no\.?|#|number|id)\s*[:#-]?\s*", "", candidate, flags=re.IGNORECASE).strip()
        if not candidate or len(candidate) > 60:
            return ""
        if re.fullmatch(DATE_RE, candidate, re.IGNORECASE):
            return ""
        if _last_money(candidate) and re.fullmatch(MONEY_RE, candidate):
            return ""
        if re.search(r"invoice|number|date|page|total|balance|project|payment|description", candidate, re.IGNORECASE):
            return ""
        if not re.search(r"\d", candidate):
            return ""
        return candidate
    return value


def _restore_camel_spacing(value: str) -> str:
    if " " in value:
        return value
    return re.sub(r"(?<=[a-z])(?=[A-Z])", " ", value)


def _clean_vendor_name(value: str) -> str:
    candidate = _clean_cell(value)
    if not candidate:
        return ""
    for separator in ["\u00b7", "\u2022", "|"]:
        if separator in candidate:
            candidate = candidate.split(separator, 1)[0].strip()
            break
    candidate = re.sub(r"\s+\S*(?:street|strasse|avenue|ave\.?|road|rd\.?|boulevard|blvd\.?)\S*.*$", "", candidate, flags=re.IGNORECASE)
    candidate = re.sub(r"\s+\d{3,}.*$", "", candidate)
    candidate = re.sub(r"\s+(?:germany|usa|united states|canada|uk|united kingdom)\b.*$", "", candidate, flags=re.IGNORECASE)
    candidate = _restore_camel_spacing(candidate.strip(" ,;:-"))
    return candidate[:120].strip()


def _field_key(field: dict[str, Any]) -> str:
    return str(field.get("key") or field.get("field_key") or "").strip()


def _field_label_candidates(field: dict[str, Any]) -> list[str]:
    key = _field_key(field)
    raw_values = [
        field.get("label"),
        field.get("exportColumn"),
        field.get("export_column"),
        key.replace("_", " "),
        field.get("hint"),
    ]
    raw_values.extend(FIELD_ALIASES.get(key, []))

    candidates: list[str] = []
    for raw in raw_values:
        text = str(raw or "").strip()
        if not text:
            continue
        candidates.append(text)
        for part in re.split(r"\s*(?:,|;|\bor\b|/)\s*", text, flags=re.IGNORECASE):
            part = part.strip()
            if len(part) >= 3:
                candidates.append(part)

    seen: set[str] = set()
    deduped: list[str] = []
    for candidate in candidates:
        normalized = _normalize_label(candidate)
        if not normalized or normalized in seen:
            continue
        seen.add(normalized)
        deduped.append(candidate)
    return deduped


def _field_kind(key: str, field_type: str) -> str:
    normalized_type = str(field_type or "text").lower()
    if key in {"subtotal", "tax", "total"} or normalized_type == "money":
        return "money"
    if key in {"invoice_date", "due_date"} or normalized_type == "date":
        return "date"
    if key == "invoice_number" or key.endswith("_number") or key.endswith("_id"):
        return "id"
    return "text"


def _extract_currency(joined_text: str) -> str:
    for symbol, code in [("$", "USD"), ("\u20ac", "EUR"), ("\u00a3", "GBP")]:
        if symbol in joined_text:
            return code
    if re.search(r"\b(EUR|EURO|USD|GBP)\b", joined_text, re.IGNORECASE):
        return re.search(r"\b(EUR|EURO|USD|GBP)\b", joined_text, re.IGNORECASE).group(1).upper().replace("EURO", "EUR")
    if re.search(r"\b(Germany|Berlin|UStG|Kleinunternehmerregelung)\b", joined_text, re.IGNORECASE):
        return "EUR"
    return ""


def _has_zero_tax_language(joined_text: str) -> bool:
    compact = _normalize_label(joined_text)
    return any(
        marker in compact
        for marker in [
            "noturnovertaxdue",
            "notaxdue",
            "novat",
            "taxexempt",
            "kleinunternehmerregelung",
            "19ustg",
        ]
    )


def _value_near_label(lines: list[str], labels: list[str], kind: str) -> str:
    normalized_labels = [_normalize_label(label) for label in labels]
    for index, line in enumerate(lines):
        cells = _split_table_row(line)
        if cells:
            normalized_cells = [_normalize_label(cell) for cell in cells]
            for cell_index, normalized_cell in enumerate(normalized_cells):
                if any(_label_matches(label, normalized_cell) for label in normalized_labels):
                    same_row_values = [_valid_value(candidate, kind) for candidate in cells[cell_index + 1:] + cells[:cell_index]]
                    same_row_values = [value for value in same_row_values if value]
                    if same_row_values:
                        return same_row_values[-1] if kind == "money" else same_row_values[0]
                    is_generic_total_header = kind == "money" and normalized_cell in {"total", "totalprice", "amount"}
                    if not is_generic_total_header:
                        for next_line in lines[index + 1:index + 5]:
                            next_cells = _split_table_row(next_line)
                            if not next_cells or all(re.fullmatch(r"[-: ]*", cell) for cell in next_cells):
                                continue
                            if cell_index < len(next_cells):
                                value = _valid_value(next_cells[cell_index], kind)
                                if value:
                                    return value
                            for candidate in next_cells:
                                value = _valid_value(candidate, kind)
                                if value:
                                    return value
            continue
        normalized_line = _normalize_label(line)
        if not any(_label_matches(label, normalized_line) for label in normalized_labels):
            continue
        if kind == "money" and "excluding" in line.lower():
            continue
        for label in labels:
            label_pattern = r"\s*".join(re.escape(part) for part in re.findall(r"[A-Za-z0-9#]+", label))
            if not label_pattern:
                continue
            match = re.search(rf"{label_pattern}\s*[:#-]?\s*(.+)$", line, re.IGNORECASE)
            if match:
                value = _valid_value(match.group(1), kind)
                if value:
                    return value
        for next_line in lines[index + 1:index + 5]:
            value = _valid_value(next_line, kind)
            if value:
                return value
    return ""


def _extract_fixed_fields(lines: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    joined = "\n".join(str(line.get("text") or "") for line in lines)
    flat = " ".join(str(line.get("text") or "") for line in lines)
    text_lines = [str(line.get("text") or "") for line in lines if str(line.get("text") or "").strip()]
    money = f"({MONEY_RE})"
    date = f"({DATE_RE})"

    invoice_number = _value_near_label(
        text_lines,
        ["invoice number", "invoice no", "invoice no.", "invoice #", "invoice id", "invoicenumber", "bill number"],
        "id",
    ) or _first_match(
        [
            r"(?:invoice|inv|bill|receipt)\s*(?:no\.?|number|#|id)\s*[:#-]?\s*([A-Z0-9#][A-Z0-9._/#-]{2,})",
            r"\b(?:no\.?|#)\s*([A-Z0-9][A-Z0-9._/-]{2,})",
        ],
        flat,
    )
    invoice_date = _value_near_label(
        text_lines,
        ["invoice date", "invoicedate", "issue date", "bill date", "date"],
        "date",
    ) or _first_match([rf"(?:invoice\s*)?date\s*[:#-]?\s*{date}"], flat)
    due_date = _value_near_label(
        text_lines,
        ["payment due date", "date payment due", "datepaymentdue", "due date", "duedate", "total due by"],
        "date",
    ) or _first_match([rf"(?:due|payment due)\s*(?:date)?\s*[:#-]?\s*{date}"], flat)
    total = _value_near_label(
        text_lines,
        ["invoice total", "total due", "amount due", "balance due", "total to pay", "total topay", "total current charges"],
        "money",
    ) or _value_near_label(
        text_lines,
        ["total price", "total"],
        "money",
    ) or _first_match([rf"(?:total due|amount due|balance due|grand total|total)\s*[:#-]?\s*{money}"], flat)
    subtotal = _value_near_label(text_lines, ["subtotal", "sub total", "basic services sub total"], "money") or _first_match([rf"(?:subtotal|sub total)\s*[:#-]?\s*{money}"], flat)
    tax = _value_near_label(text_lines, ["vat total", "vattotale", "taxes&surcharges", "sales tax", "tax rate", "taxrate", "taxes", "tax", "gst"], "money") or _first_match([rf"(?:tax|gst)\s*[:#-]?\s*{money}"], flat)
    if tax and not re.search(r"[\$\u20ac\u00a3]|[\.,]\d{2}", tax):
        tax = ""
    for line in lines:
        line_text = re.sub(r"\s+", " ", str(line.get("text") or "")).strip()
        if not total and re.search(r"\b(invoice total|total due|amount due|balance due|grand total|total)\b", line_text, re.IGNORECASE):
            total = _last_money(line_text)
        if not subtotal and re.search(r"\b(subtotal|sub total)\b", line_text, re.IGNORECASE):
            subtotal = _last_money(line_text)
        if not tax and re.search(r"\b(tax|vat|gst)\b", line_text, re.IGNORECASE):
            tax = _last_money(line_text)

    vendor = ""
    for line in text_lines[:14]:
        candidate = re.sub(r"\s+", " ", line).strip()
        if (
            len(candidate) >= 3
            and re.search(r"[A-Za-z]", candidate)
            and not re.search(r"invoice|tax invoice|date|receipt|bill to|ship to|total|project details|recipientname", candidate, re.IGNORECASE)
            and not re.fullmatch(r"\d+\s*\(\d+\)", candidate)
        ):
            vendor = _clean_vendor_name(candidate)
            break

    def confidence_for(value: str) -> float | None:
        if not value:
            return None
        scores = [
            line.get("confidence")
            for line in lines
            if line.get("confidence") is not None and value.lower() in str(line.get("text") or "").lower()
        ]
        scores = [float(score) for score in scores if isinstance(score, (int, float))]
        if not scores:
            return 0.72
        return min(1.0, max(0.0, sum(scores) / len(scores)))

    currency = _extract_currency(joined)

    return {
        "vendor_name": {"value": vendor, "confidence": confidence_for(vendor)},
        "invoice_number": {"value": invoice_number, "confidence": confidence_for(invoice_number)},
        "invoice_date": {"value": invoice_date, "confidence": confidence_for(invoice_date)},
        "due_date": {"value": due_date, "confidence": confidence_for(due_date)},
        "subtotal": {"value": _normalize_money(subtotal), "confidence": confidence_for(subtotal)},
        "tax": {"value": _normalize_money(tax), "confidence": confidence_for(tax)},
        "total": {"value": _normalize_money(total), "confidence": confidence_for(total)},
        "currency": {"value": currency, "confidence": 0.7 if currency else None},
    }


def _line_value_after_label(label: str, lines: list[dict[str, Any]]) -> str:
    safe_label = re.escape(label.strip())
    for line in lines:
        text = re.sub(r"\s+", " ", str(line.get("text") or "")).strip()
        match = re.search(rf"\b{safe_label}\b\s*[:#-]?\s*(.+)$", text, re.IGNORECASE)
        if match:
            value = match.group(1).strip()
            if value and value.lower() != label.lower():
                return value[:120]
    return ""


def _field_region(value: str, blocks: list[dict[str, Any]]) -> dict[str, Any] | None:
    needle = re.sub(r"\s+", " ", str(value or "")).strip().lower()
    if not needle:
        return None
    compact_needle = _normalize_label(needle)
    for block in blocks:
        text = re.sub(r"\s+", " ", str(block.get("text") or "")).strip().lower()
        compact_text = _normalize_label(text)
        if needle in text or (compact_needle and compact_needle in compact_text):
            return {
                "blockId": block.get("id"),
                "page": block.get("page") or 1,
                "pageWidth": block.get("pageWidth"),
                "pageHeight": block.get("pageHeight"),
                "box": block.get("box"),
                "text": block.get("text"),
            }
    return None


def _shape_fields(
    lines: list[dict[str, Any]],
    blocks: list[dict[str, Any]],
    requested_fields: list[dict[str, Any]] | None,
    source_name: str,
) -> dict[str, dict[str, Any]]:
    fixed = _extract_fixed_fields(lines)
    wanted = requested_fields or DEFAULT_TEMPLATE_FIELDS
    text_lines = [str(line.get("text") or "") for line in lines if str(line.get("text") or "").strip()]
    joined_text = "\n".join(text_lines)
    shaped: dict[str, dict[str, Any]] = {}
    for field in wanted:
        key = _field_key(field)
        if not key:
            continue
        aliases = _field_label_candidates(field)
        field_type = str(field.get("type") or "text").lower()
        kind = _field_kind(key, field_type)
        value = _value_near_label(text_lines, aliases, kind) if aliases else ""
        confidence: float | None = 0.58 if value else None

        if key == "vendor_name":
            value = _clean_vendor_name(value) if value else ""
        if key == "currency" and not value:
            value = _extract_currency(joined_text)
            confidence = 0.64 if value else None
        if key == "tax" and not value and _has_zero_tax_language(joined_text):
            value = "0.00"
            confidence = 0.66

        direct = fixed.get(key) or {}
        if not value:
            value = str(direct.get("value") or "").strip()
            confidence = direct.get("confidence")
            if key == "vendor_name":
                value = _clean_vendor_name(value)
        if key == "subtotal" and not value:
            total_value = str((fixed.get("total") or {}).get("value") or "").strip()
            tax_value = str((fixed.get("tax") or {}).get("value") or "").strip()
            if total_value and (tax_value in {"0", "0.0", "0.00"} or _has_zero_tax_language(joined_text)):
                value = total_value
                confidence = 0.62
        if key == "tax" and value and not re.search(r"[\$\u20ac\u00a3]|[\.,]\d{2}", value):
            value = ""
        if key == "tax" and not value and _has_zero_tax_language(joined_text):
            value = "0.00"
            confidence = 0.66
        if key == "tax" and value in {"0", "0.0"}:
            value = "0.00"
        if value and confidence is None:
            confidence = 0.58
        region = _field_region(value, blocks)
        shaped[key] = {
            "value": value,
            "confidence": confidence if value else None,
            "region": region,
            "source": source_name if value else "missing",
            "required": bool(field.get("required")),
        }
    return shaped


def _convert_with_layout(input_path: Path, mode: str, selected_device: str, template_fields: list[dict[str, Any]] | None, started: float) -> dict[str, Any]:
    result = _converter("layout", mode, selected_device).convert(input_path)
    return _shape_docling_result(
        result.document,
        mode,
        selected_device,
        template_fields,
        started,
        "docling-layout-rapidocr",
        "docling_layout",
        [],
    )


def _shape_docling_result(
    doc: Any,
    mode: str,
    selected_device: str,
    template_fields: list[dict[str, Any]] | None,
    started: float,
    engine: str,
    source_name: str,
    errors: list[str],
) -> dict[str, Any]:
    payload = _as_dict(doc)
    text = doc.export_to_text()
    markdown = doc.export_to_markdown()
    blocks = _collect_blocks(payload, text or markdown)
    lines = [
        {
            "text": str(block.get("text") or ""),
            "confidence": block.get("confidence"),
            "box": block.get("box"),
            "page": block.get("page") or 1,
        }
        for block in blocks
    ]
    fields = _shape_fields(lines, blocks, template_fields, source_name)
    return {
        "ok": True,
        "engine": engine,
        "mode": "light" if mode == "light" else "standard",
        "device": selected_device,
        "model": GRANITE_REPO_ID if "granite" in engine else "docling-local-layout",
        "text": text,
        "markdown": markdown,
        "pages": _pages(payload),
        "blocks": blocks,
        "lines": lines,
        "fields": fields,
        "tables": _collect_tables(payload),
        "errors": errors,
        "timings": {"totalSeconds": round(time.perf_counter() - started, 3)},
        "raw": payload,
    }


def _has_field_value(field: Any) -> bool:
    return isinstance(field, dict) and bool(str(field.get("value") or "").strip())


def _merge_layout_fields(layout_fields: Any, standard_fields: Any) -> dict[str, dict[str, Any]]:
    """Prefer layout/OCR fields; use Standard VLM fields only to fill blanks."""
    merged: dict[str, dict[str, Any]] = {}
    if isinstance(standard_fields, dict):
        merged.update({str(key): dict(value) for key, value in standard_fields.items() if isinstance(value, dict)})
    if not isinstance(layout_fields, dict):
        return merged

    for key, value in layout_fields.items():
        if not isinstance(value, dict):
            continue
        key_text = str(key)
        if _has_field_value(value) or not _has_field_value(merged.get(key_text)):
            merged[key_text] = dict(value)
    return merged


def run_docling_extract(
    input_path: Path,
    mode: str = "standard",
    device: str = "auto",
    template_fields: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    started = time.perf_counter()
    selected_device = _requested_device(device)
    selected_mode = "light" if mode == "light" else "standard"
    source_path, cleanup_path = _prepare_low_memory_source(input_path)
    try:
        if selected_mode == "light" or os.getenv("INVOICE_TIDY_DOCLING_LAYOUT_ONLY", "0") == "1":
            return _convert_with_layout(source_path, mode, selected_device, template_fields, started)

        errors: list[str] = []
        layout_baseline: dict[str, Any] | None = None
        if os.getenv("INVOICE_TIDY_STANDARD_LAYOUT_FIELDS", "1") != "0":
            try:
                layout_baseline = _convert_with_layout(source_path, "light", selected_device, template_fields, started)
            except Exception as exc:
                errors.append(f"Layout field baseline failed: {exc}")

        result = _converter("granite", mode, selected_device).convert(source_path)
        shaped = _shape_docling_result(
            result.document,
            mode,
            selected_device,
            template_fields,
            started,
            "docling-granite-258m",
            "docling_granite_layout",
            errors,
        )
        if layout_baseline:
            shaped["fields"] = _merge_layout_fields(layout_baseline.get("fields"), shaped.get("fields"))
            if not shaped.get("tables") and layout_baseline.get("tables"):
                shaped["tables"] = layout_baseline["tables"]
            shaped["fieldEngine"] = layout_baseline.get("engine") or "docling-layout-rapidocr"
        if shaped["blocks"] or str(shaped.get("text") or "").strip() or str(shaped.get("markdown") or "").strip():
            return shaped
        errors.append("Granite Docling returned an empty document; used Docling local OCR/layout fallback.")
        fallback = layout_baseline or _convert_with_layout(source_path, "light", selected_device, template_fields, started)
        fallback["errors"] = errors
        fallback["engine"] = "docling-layout-rapidocr:fallback"
        fallback["mode"] = "standard"
        return fallback
    finally:
        if cleanup_path:
            try:
                cleanup_path.unlink(missing_ok=True)
            except OSError:
                pass


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("input_path")
    parser.add_argument("--mode", choices=["light", "standard"], default="standard")
    parser.add_argument("--device", choices=["auto", "cpu", "gpu", "gpu:0", "cuda"], default="auto")
    args = parser.parse_args()
    payload = run_docling_extract(Path(args.input_path), args.mode, args.device)
    print(json.dumps(payload, ensure_ascii=False, default=_json_default))
    return 0 if payload.get("ok") else 1


if __name__ == "__main__":
    raise SystemExit(main())
