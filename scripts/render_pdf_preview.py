#!/usr/bin/env python
"""Render a PDF page to a PNG data URI for the local review preview."""

from __future__ import annotations

import argparse
import base64
import io
from pathlib import Path

import pypdfium2 as pdfium


def render_page(path: Path, page_number: int, scale: float) -> str:
    pdf = pdfium.PdfDocument(str(path))
    try:
        if len(pdf) == 0:
            raise ValueError("PDF has no pages")

        page_index = max(0, min(page_number - 1, len(pdf) - 1))
        page = pdf[page_index]
        try:
            bitmap = page.render(scale=scale)
            image = bitmap.to_pil()
        finally:
            page.close()
    finally:
        pdf.close()

    buffer = io.BytesIO()
    image.save(buffer, format="PNG", optimize=True)
    encoded = base64.b64encode(buffer.getvalue()).decode("ascii")
    return f"data:image/png;base64,{encoded}"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("pdf_path")
    parser.add_argument("--page", type=int, default=1)
    parser.add_argument("--scale", type=float, default=2.0)
    args = parser.parse_args()

    print(render_page(Path(args.pdf_path), args.page, args.scale), end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
