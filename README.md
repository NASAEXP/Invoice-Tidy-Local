# Invoice Tidy Local

Offline-first Windows desktop app for local invoice ingestion, OCR extraction, review, and CSV export.

Built with **Go + Wails v2**, **Alpine.js**, and a **Python Docling/FastAPI** worker daemon.

Files never leave your machine — no cloud upload, no account, no subscription.

## Download

Prebuilt Windows binaries: **[GitHub Releases](https://github.com/NASAEXP/Invoice-Tidy-Local/releases/latest)**.

Windows 10/11 (64-bit) only. macOS and Linux are not yet supported.

## Requirements (building from source)

- Windows 10/11 (64-bit)
- Go matching `go.mod` (currently 1.26)
- [Wails v2](https://wails.io/) CLI
- Node.js (for the Tailwind CSS build)
- Python 3.10–3.13 (the venv is installed automatically on first run via `scripts/setup-docling.ps1`)

## Quick start

```powershell
# Build frontend CSS after changing HTML/classes
cd frontend
npm install
npm run build:css

# Run in dev
wails dev

# Build release binary
wails build
```

## First run

On startup the app:

1. Opens instantly with bundled offline UI assets (no CDN)
2. Initializes SQLite in `%AppData%\invoice-tidy\local\`
3. Starts the Python extraction daemon (15–45s model warmup is normal)

If Docling dependencies are missing, the setup overlay runs `scripts/setup-docling.ps1` (~2GB download). After this one-time setup the app runs fully offline.

## Performance

Extraction is local ML inference, so speed depends on hardware:

- **CPU** — works, but expect **minutes per document** on large/scanned files.
- **CUDA GPU** — much faster. Run setup with the `-Cuda` flag (`scripts/setup-docling.ps1 -Cuda`) or set `INVOICE_TIDY_USE_CUDA=1`.

The first extraction after launch also pays a one-time model warmup (15–45s).

## Data locations

| Path | Purpose |
|------|---------|
| `%AppData%\invoice-tidy\local\invoice-tidy-local.sqlite` | Document metadata |
| `%AppData%\invoice-tidy\local\documents\` | Imported invoice files |
| `%AppData%\invoice-tidy\local\daemon.log` | Python worker logs |

View live paths in **Daemon Settings** inside the app.

## Project layout

```
frontend/src/     HTML + Alpine.js UI (embedded in Go binary)
scripts/          Python worker + setup scripts
local-tools/      Python venv + models (gitignored, created locally)
```

## License

Apache License 2.0 — see [LICENSE](LICENSE).

Bundled and downloaded third-party components (Docling, Granite, RapidOCR,
PyTorch, etc.) keep their own licenses — see [NOTICE](NOTICE).
