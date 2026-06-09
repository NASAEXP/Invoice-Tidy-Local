import unittest
from unittest.mock import patch

import os
import tempfile
from pathlib import Path

from docling_extract import _merge_layout_fields, _prepare_low_memory_source, _shape_fields, run_docling_extract, warm_docling_converter


class FakeConverter:
    def __init__(self):
        self.initialized = []
        self.converted = []

    def initialize_pipeline(self, input_format):
        self.initialized.append(input_format)

    def convert(self, source):
        self.converted.append(source)


class TestDoclingWarmup(unittest.TestCase):
    def test_warmup_initializes_docling_pipelines_without_converting_blank_file(self):
        fake = FakeConverter()

        with patch("docling_extract.ensure_local_env"), patch(
            "docling_extract._requested_device",
            return_value="cpu",
        ), patch("docling_extract._converter", return_value=fake):
            warm_docling_converter("light", "cpu")

        self.assertEqual(len(fake.initialized), 2)
        self.assertEqual(fake.converted, [])

    def test_light_mode_uses_layout_converter_without_granite_first(self):
        calls = []

        def fake_convert_with_layout(input_path, mode, selected_device, template_fields, started):
            calls.append(("layout", str(input_path), mode, selected_device, template_fields is None))
            return {"ok": True, "engine": "docling-layout-rapidocr", "tables": []}

        def fail_granite(*args, **kwargs):
            raise AssertionError("light mode should not initialize Granite")

        with patch("docling_extract._requested_device", return_value="cpu"), patch(
            "docling_extract._convert_with_layout",
            fake_convert_with_layout,
        ), patch("docling_extract._converter", fail_granite):
            result = run_docling_extract(Path("invoice.png"), "light", "cpu")

        self.assertEqual(result["engine"], "docling-layout-rapidocr")
        self.assertEqual(calls, [("layout", "invoice.png", "light", "cpu", True)])

    def test_large_image_is_downscaled_before_docling_conversion(self):
        try:
            from PIL import Image
        except Exception:
            self.skipTest("Pillow is not installed")

        with tempfile.TemporaryDirectory() as tmpdir:
            source = Path(tmpdir) / "test-large-invoice.png"
            Image.new("RGB", (1984, 1824), "white").save(source)
            with patch.dict(os.environ, {"INVOICE_TIDY_MAX_IMAGE_SIDE": "1000"}):
                prepared, cleanup = _prepare_low_memory_source(source)

            self.assertIsNotNone(cleanup)
            self.assertNotEqual(prepared, source)
            with Image.open(prepared) as image:
                self.assertLessEqual(max(image.size), 1000)
            if "cleanup" in locals() and cleanup:
                cleanup.unlink(missing_ok=True)

    def test_standard_field_merge_prefers_layout_ocr_values(self):
        layout_fields = {
            "vendor_name": {"value": "Fountainhead A+E", "confidence": 0.72, "source": "docling_layout"},
            "invoice_number": {"value": "GALT-009", "confidence": 0.72, "source": "docling_layout"},
            "due_date": {"value": "", "confidence": None, "source": "missing"},
        }
        standard_fields = {
            "vendor_name": {"value": "MONTHLY INVOICE", "confidence": 0.58, "source": "docling_granite_layout"},
            "invoice_number": {"value": "", "confidence": None, "source": "missing"},
            "due_date": {"value": "Aug 31, 2013", "confidence": 0.58, "source": "docling_granite_layout"},
        }

        merged = _merge_layout_fields(layout_fields, standard_fields)

        self.assertEqual(merged["vendor_name"]["value"], "Fountainhead A+E")
        self.assertEqual(merged["invoice_number"]["value"], "GALT-009")
        self.assertEqual(merged["due_date"]["value"], "Aug 31, 2013")


class TestTemplateFieldExtraction(unittest.TestCase):
    def test_extracts_custom_field_using_export_column_and_hint_aliases(self):
        lines = [{"text": "Purchase Order Number: PO-8842", "confidence": None, "page": 1, "box": None}]
        fields = [
            {
                "key": "po_number",
                "label": "PO #",
                "type": "text",
                "required": False,
                "hint": "Purchase order number",
                "exportColumn": "PO Number",
            }
        ]

        result = _shape_fields(lines, lines, fields, "test_source")

        self.assertEqual(result["po_number"]["value"], "PO-8842")

    def test_invoice_number_template_field_uses_id_rules_even_when_type_is_text(self):
        lines = [
            {"text": "Invoice ID:", "confidence": None, "page": 1, "box": None},
            {"text": "4736", "confidence": None, "page": 1, "box": None},
        ]
        fields = [
            {
                "key": "invoice_number",
                "label": "Invoice #",
                "type": "text",
                "required": True,
                "hint": "Invoice ID, invoice number, receipt number",
                "exportColumn": "Invoice Number",
            }
        ]

        result = _shape_fields(lines, lines, fields, "test_source")

        self.assertEqual(result["invoice_number"]["value"], "4736")

    def test_template_date_handles_ocr_compacted_month_day(self):
        lines = [
            {"text": "Issue date:", "confidence": None, "page": 1, "box": None},
            {"text": "August9,2019", "confidence": None, "page": 1, "box": None},
        ]
        fields = [
            {
                "key": "invoice_date",
                "label": "Invoice date",
                "type": "date",
                "required": True,
                "hint": "Invoice issue date",
                "exportColumn": "Invoice Date",
            }
        ]

        result = _shape_fields(lines, lines, fields, "test_source")

        self.assertEqual(result["invoice_date"]["value"], "August 9, 2019")

    def test_vendor_template_field_cleans_joined_address(self):
        lines = [
            {
                "text": "TimoDenk\u00b7SomeStreet82\u00b710000Berlin,Germany",
                "confidence": None,
                "page": 1,
                "box": None,
            }
        ]
        fields = [
            {
                "key": "vendor_name",
                "label": "Vendor",
                "type": "text",
                "required": True,
                "hint": "Vendor or supplier name",
                "exportColumn": "Vendor",
            }
        ]

        result = _shape_fields(lines, lines, fields, "test_source")

        self.assertEqual(result["vendor_name"]["value"], "Timo Denk")

    def test_tax_field_uses_zero_when_invoice_says_no_turnover_tax(self):
        lines = [
            {"text": "ThisinvoicehasnoturnovertaxduetoKleinunternehmerregelung accordingto19UStG.", "confidence": None, "page": 1, "box": None},
            {"text": "Taxnumber:", "confidence": None, "page": 1, "box": None},
            {"text": "35061/00029", "confidence": None, "page": 1, "box": None},
        ]
        fields = [
            {
                "key": "tax",
                "label": "Tax",
                "type": "money",
                "required": False,
                "hint": "Tax amount",
                "exportColumn": "Tax",
            }
        ]

        result = _shape_fields(lines, lines, fields, "test_source")

        self.assertEqual(result["tax"]["value"], "0.00")


if __name__ == "__main__":
    unittest.main()
