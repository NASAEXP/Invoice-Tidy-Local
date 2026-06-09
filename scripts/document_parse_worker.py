#!/usr/bin/env python
"""Persistent local document parser for Invoice Tidy Local."""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import secrets
import sys
import time
from pathlib import Path
from typing import Any, Literal

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

from fastapi import BackgroundTasks, FastAPI, Header, HTTPException
from pydantic import BaseModel, Field

try:
    import uvicorn
except Exception:  # pragma: no cover - setup check reports this clearly
    uvicorn = None

SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

from docling_extract import (  # noqa: E402
    DEFAULT_TEMPLATE_FIELDS,
    _json_default,
    _requested_device,
    ensure_granite_model_local,
    ensure_local_env,
    run_docling_extract,
    warm_docling_converter,
)


class TemplateField(BaseModel):
    key: str
    label: str
    type: str = "text"
    required: bool = False
    hint: str | None = None
    exportColumn: str | None = None


class JobRequest(BaseModel):
    documentPath: str
    documentId: str
    mode: Literal["auto", "light", "standard"] = "auto"
    templateFields: list[TemplateField] = Field(default_factory=list)


class WorkerState:
    def __init__(self, requested_device: str) -> None:
        self.requested_device = requested_device
        self.device = _requested_device(requested_device)
        self.model_status: dict[str, Any] = {
            "worker": "starting",
            "light": "not_loaded",
            "standard": "not_loaded",
            "device": self.device,
            "engine": "docling-granite-258m",
            "model": "ibm-granite/granite-docling-258M",
        }
        self.jobs: dict[str, dict[str, Any]] = {}
        self.queue: asyncio.Queue[str] = asyncio.Queue()
        self.lock = asyncio.Lock()
        self.started_at = time.time()


def _require_token(expected: str, authorization: str | None) -> None:
    token = ""
    if authorization and authorization.lower().startswith("bearer "):
        token = authorization[7:].strip()
    if not token or not secrets.compare_digest(token, expected):
        raise HTTPException(status_code=401, detail="Unauthorized")


def _missing_dependency_error(exc: Exception) -> dict[str, Any]:
    return {
        "ok": False,
        "missingDependency": True,
        "engine": "docling-granite-258m",
        "device": "unknown",
        "model": "ibm-granite/granite-docling-258M",
        "error": f"Docling worker dependencies are not installed: {exc}",
        "errors": [f"Docling worker dependencies are not installed: {exc}"],
    }


def _parse_document(path: Path, mode: str, requested_device: str, fields: list[TemplateField]) -> dict[str, Any]:
    started = time.perf_counter()
    if not path.exists():
        return {"ok": False, "error": "Input file does not exist.", "errors": ["Input file does not exist."]}
    selected_mode = "light" if mode == "light" else "standard"
    selected_device = _requested_device(requested_device)
    try:
        import docling  # noqa: F401
        import torch  # noqa: F401
        import transformers  # noqa: F401
        import rapidocr  # noqa: F401
        import onnxruntime  # noqa: F401
    except Exception as exc:
        return _missing_dependency_error(exc)

    try:
        ensure_local_env()
        if os.getenv("INVOICE_TIDY_DOCLING_LAYOUT_ONLY", "0") != "1":
            ensure_granite_model_local(download=os.getenv("INVOICE_TIDY_GRANITE_AUTO_DOWNLOAD", "1") != "0")
        requested_fields = [field.model_dump() for field in fields] if fields else DEFAULT_TEMPLATE_FIELDS
        result = run_docling_extract(path, selected_mode, selected_device, requested_fields)
        result["timings"] = {
            **(result.get("timings") or {}),
            "totalSeconds": round(time.perf_counter() - started, 3),
        }
        return result
    except Exception as exc:
        return {
            "ok": False,
            "engine": "docling-layout-rapidocr",
            "device": selected_device,
            "model": "docling-local-layout",
            "error": str(exc),
            "errors": [str(exc)],
            "timings": {"totalSeconds": round(time.perf_counter() - started, 3)},
        }


async def _job_loop(state: WorkerState) -> None:
    while True:
        job_id = await state.queue.get()
        job = state.jobs.get(job_id)
        if not job:
            state.queue.task_done()
            continue
        job["status"] = "running"
        job["startedAt"] = time.time()
        payload: JobRequest = job["request"]
        selected_mode = "light" if payload.mode == "light" else "standard"
        try:
            async with state.lock:
                state.model_status["worker"] = "busy"
                result = await asyncio.to_thread(
                    _parse_document,
                    Path(payload.documentPath),
                    payload.mode,
                    state.requested_device,
                    payload.templateFields,
                )
            job["status"] = "succeeded" if result.get("ok") else "failed"
            job["result"] = result
            state.model_status["device"] = result.get("device") or state.model_status.get("device")
            state.model_status[selected_mode] = "loaded" if result.get("ok") else "failed"
        except Exception as exc:
            job["status"] = "failed"
            job["result"] = {"ok": False, "error": str(exc), "errors": [str(exc)]}
            state.model_status[selected_mode] = "failed"
        finally:
            job["finishedAt"] = time.time()
            state.model_status["worker"] = "ready"
            state.queue.task_done()


async def _warmup_worker(state: WorkerState) -> None:
    if os.getenv("INVOICE_TIDY_DOCLING_SKIP_WARMUP", "0") == "1":
        state.model_status["worker"] = "ready"
        return

    state.model_status["worker"] = "warming"
    errors: list[str] = []
    try:
        for mode in ("light", "standard"):
            try:
                async with state.lock:
                    state.model_status["worker"] = "warming"
                    await asyncio.to_thread(warm_docling_converter, mode, state.requested_device)
                    state.model_status[mode] = "loaded"
            except Exception as exc:
                state.model_status[mode] = "failed"
                errors.append(f"{mode}: {exc}")
        if errors:
            state.model_status["warmup_error"] = "; ".join(errors)
    finally:
        state.model_status["worker"] = "ready"


def create_app(token: str, requested_device: str) -> FastAPI:
    app = FastAPI(title="Invoice Tidy Local Parser", version="0.2.0")
    state = WorkerState(requested_device)

    @app.on_event("startup")
    async def startup() -> None:
        asyncio.create_task(_job_loop(state))
        asyncio.create_task(_warmup_worker(state))

    @app.get("/health")
    async def health(authorization: str | None = Header(default=None)) -> dict[str, Any]:
        _require_token(token, authorization)
        return {
            "ok": True,
            "status": state.model_status["worker"],
            "device": state.model_status["device"],
            "engine": state.model_status["engine"],
            "model": state.model_status["model"],
            "uptimeSeconds": round(time.time() - state.started_at, 3),
            "queue": state.queue.qsize(),
        }

    @app.get("/models")
    async def models(authorization: str | None = Header(default=None)) -> dict[str, Any]:
        _require_token(token, authorization)
        return {"ok": True, **state.model_status}

    @app.post("/jobs")
    async def create_job(payload: JobRequest, authorization: str | None = Header(default=None)) -> dict[str, Any]:
        _require_token(token, authorization)
        job_id = payload.documentId or secrets.token_hex(12)
        state.jobs[job_id] = {
            "id": job_id,
            "status": "queued",
            "createdAt": time.time(),
            "request": payload,
            "result": None,
        }
        await state.queue.put(job_id)
        return {"ok": True, "id": job_id, "status": "queued"}

    @app.get("/jobs/{job_id}")
    async def get_job(job_id: str, authorization: str | None = Header(default=None)) -> dict[str, Any]:
        _require_token(token, authorization)
        job = state.jobs.get(job_id)
        if not job:
            raise HTTPException(status_code=404, detail="Job not found")
        return {
            "ok": True,
            "id": job["id"],
            "status": job["status"],
            "createdAt": job.get("createdAt"),
            "startedAt": job.get("startedAt"),
            "finishedAt": job.get("finishedAt"),
            "result": job.get("result"),
        }

    @app.post("/shutdown")
    async def shutdown(background_tasks: BackgroundTasks, authorization: str | None = Header(default=None)) -> dict[str, Any]:
        _require_token(token, authorization)

        def stop() -> None:
            time.sleep(0.2)
            os._exit(0)

        background_tasks.add_task(stop)
        return {"ok": True}

    return app


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--token", required=True)
    parser.add_argument("--device", default="auto", choices=["auto", "cpu", "gpu", "gpu:0", "cuda"])
    args = parser.parse_args()

    if args.host not in {"127.0.0.1", "localhost"}:
        print(json.dumps({"ok": False, "error": "Worker may only bind to localhost."}, ensure_ascii=True))
        return 2
    if uvicorn is None:
        print(json.dumps({"ok": False, "error": "FastAPI worker dependencies are missing. Run npm run local:docling:setup."}, ensure_ascii=True))
        return 2
    app = create_app(args.token, args.device)
    print(
        json.dumps(
            {
                "ok": True,
                "url": f"http://{args.host}:{args.port}",
                "device": _requested_device(args.device),
                "engine": "docling-layout-rapidocr",
                "model": "docling-local-layout",
            },
            ensure_ascii=True,
            default=_json_default,
        ),
        flush=True,
    )
    uvicorn.run(app, host=args.host, port=args.port, log_level="warning")
    return 0


if __name__ == "__main__":
    sys.exit(main())
