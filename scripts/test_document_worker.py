import unittest
from pathlib import Path
import sys
import asyncio
import os
from unittest.mock import patch

# Add scripts dir to path
sys.path.insert(0, str(Path(__file__).resolve().parent))

from document_parse_worker import WorkerState, _warmup_worker

class TestWorkerState(unittest.TestCase):
    def test_worker_state_initialization(self):
        state = WorkerState("cpu")
        self.assertEqual(state.model_status["worker"], "starting")
        self.assertEqual(state.model_status["light"], "not_loaded")
        self.assertEqual(state.model_status["standard"], "not_loaded")
        self.assertEqual(state.model_status["device"], "cpu")

    def test_warmup_serializes_converter_access_with_parse_lock(self):
        state = WorkerState("cpu")
        observed = []

        def fake_warmup(mode, device):
            observed.append((mode, state.lock.locked()))

        with patch.dict(os.environ, {"INVOICE_TIDY_DOCLING_SKIP_WARMUP": "0"}), patch(
            "document_parse_worker.warm_docling_converter",
            fake_warmup,
        ):
            asyncio.run(_warmup_worker(state))

        self.assertEqual([("light", True), ("standard", True)], observed)
        self.assertEqual(state.model_status["light"], "loaded")
        self.assertEqual(state.model_status["standard"], "loaded")
        self.assertEqual(state.model_status["worker"], "ready")

if __name__ == "__main__":
    unittest.main()
