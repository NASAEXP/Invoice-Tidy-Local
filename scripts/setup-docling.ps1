param(
  [switch]$CpuOnly,
  [switch]$Cuda,
  [switch]$DownloadGranite
)

$ErrorActionPreference = "Stop"

$pythonThreads = if ($env:INVOICE_TIDY_PYTHON_THREADS) { $env:INVOICE_TIDY_PYTHON_THREADS } else { "1" }
foreach ($key in @("OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MKL_NUM_THREADS", "VECLIB_MAXIMUM_THREADS", "NUMEXPR_NUM_THREADS", "NUMEXPR_MAX_THREADS")) {
  Set-Item -Path "Env:$key" -Value $pythonThreads
}
$env:TOKENIZERS_PARALLELISM = if ($env:TOKENIZERS_PARALLELISM) { $env:TOKENIZERS_PARALLELISM } else { "false" }

$repoRoot = Split-Path -Parent $PSScriptRoot
$venvPath = Join-Path $repoRoot "local-tools\docling-venv"
$pythonPath = Join-Path $venvPath "Scripts\python.exe"
$hfCache = Join-Path $repoRoot "local-tools\hf-cache"
$doclingModels = Join-Path $repoRoot "local-tools\docling-models"
$graniteModel = Join-Path $doclingModels "ibm-granite--granite-docling-258M"

function Find-CompatiblePython {
  $candidates = @(
    @("py", "-3.13"),
    @("py", "-3.12"),
    @("py", "-3.11"),
    @("py", "-3.10"),
    @("python")
  )

  foreach ($candidate in $candidates) {
    $exe = $candidate[0]
    $args = @()
    if ($candidate.Length -gt 1) { $args = $candidate[1..($candidate.Length - 1)] }
    try {
      $version = & $exe @args -c "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')" 2>$null
      if ($LASTEXITCODE -eq 0 -and $version -match "^(3\.10|3\.11|3\.12|3\.13)$") {
        return @{ Exe = $exe; Args = $args; Version = $version }
      }
    } catch {
    }
  }
  return $null
}

function Get-NvidiaDriverMajor {
  try {
    $driver = & nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>$null | Select-Object -First 1
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($driver)) {
      return $null
    }
    return ($driver.Trim().Split(".")[0] -as [int])
  } catch {
    return $null
  }
}

$python = Find-CompatiblePython
if ($null -eq $python) {
  throw "Docling needs Python 3.10-3.13 on Windows. Install Python 3.12, then rerun npm run local:docling:setup."
}

if (!(Test-Path $pythonPath)) {
  & $python.Exe @($python.Args + @("-m", "venv", $venvPath))
}
New-Item -ItemType Directory -Force -Path $hfCache | Out-Null
New-Item -ItemType Directory -Force -Path $doclingModels | Out-Null

& $pythonPath -m pip install --upgrade pip

$useCudaTorch = $Cuda -and -not $CpuOnly
if (-not $useCudaTorch) {
  Write-Host "Installing the CPU Torch runtime. Set INVOICE_TIDY_USE_CUDA=1 only if this machine has enough GPU/system memory for CUDA Torch."
  & $pythonPath -m pip install --force-reinstall torch torchvision --index-url https://download.pytorch.org/whl/cpu
} else {
  $driverMajor = Get-NvidiaDriverMajor
  if ($null -eq $driverMajor) {
    Write-Warning "No NVIDIA driver was detected. Installing the CPU Torch runtime."
    & $pythonPath -m pip install --force-reinstall torch torchvision --index-url https://download.pytorch.org/whl/cpu
  } else {
    Write-Host "Installing the CUDA Torch runtime because -Cuda was requested."
    & $pythonPath -m pip install --force-reinstall torch torchvision --index-url https://download.pytorch.org/whl/cu126
  }
}

& $pythonPath -m pip install "docling[rapidocr]>=2.93.0" rapidocr onnxruntime "transformers>=4.57.0" accelerate safetensors huggingface_hub fastapi uvicorn pydantic

$env:HF_HOME = if ($env:HF_HOME) { $env:HF_HOME } else { $hfCache }
$env:TRANSFORMERS_CACHE = if ($env:TRANSFORMERS_CACHE) { $env:TRANSFORMERS_CACHE } else { $hfCache }
$env:HF_HUB_DISABLE_SYMLINKS_WARNING = if ($env:HF_HUB_DISABLE_SYMLINKS_WARNING) { $env:HF_HUB_DISABLE_SYMLINKS_WARNING } else { "1" }

if ($DownloadGranite) {
  & $pythonPath -c "from huggingface_hub import snapshot_download; snapshot_download(repo_id='ibm-granite/granite-docling-258M', revision='main', local_dir=r'$graniteModel', max_workers=2)"
  if ($LASTEXITCODE -ne 0) {
    throw "Granite Docling model download failed."
  }
}

& $pythonPath -c "import docling, torch, transformers, fastapi, uvicorn, pydantic, rapidocr, onnxruntime; print('cuda=', torch.cuda.is_available())"
if ($LASTEXITCODE -ne 0) {
  throw "Docling install check failed."
}

Write-Host "Docling worker installed at $pythonPath"
Write-Host "Python version: $($python.Version)"
Write-Host "Hugging Face cache: $hfCache"
Write-Host "Docling model folder: $doclingModels"
if ($DownloadGranite) {
  Write-Host "Granite Docling local model folder: $graniteModel"
} else {
  Write-Host "Granite Docling download skipped. Add -DownloadGranite only if you want to test the optional VLM path."
}
Write-Host "Set INVOICE_TIDY_DOCLING_PYTHON=$pythonPath if you want to force this interpreter."
Write-Host "Set INVOICE_TIDY_DOCLING_DEVICE=cpu if you want to force CPU mode."
Write-Host "Set INVOICE_TIDY_USE_CUDA=1 before setup only if you want the larger CUDA Torch runtime."
