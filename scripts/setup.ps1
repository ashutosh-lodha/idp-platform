# For Windows systems, this PowerShell script sets up the Mini-IDP environment by checking for necessary tools, starting Minikube, and launching the backend and Cloudflare tunnel.
Write-Host "🚀 Starting Mini-IDP Setup..." -ForegroundColor Cyan

# ---------------- MOVE TO ROOT ----------------
$root = Resolve-Path "$PSScriptRoot\.."
Set-Location $root

# ---------------- CHECK COMMAND ----------------
function Check-Command {
    param ($name)
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        Write-Host "❌ $name not found. Install it first." -ForegroundColor Red
        exit 1
    }
}

Check-Command "minikube"
Check-Command "kubectl"
Check-Command "docker"
Check-Command "go"
Check-Command "cloudflared"

# ---------------- CHECK DOCKER RUNNING ----------------
Write-Host "🐳 Checking Docker..." -ForegroundColor Cyan
docker info > $null 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Host "❌ Docker is not running. Start Docker Desktop." -ForegroundColor Red
    exit 1
}
Write-Host "✅ Docker running" -ForegroundColor Green

# ---------------- CHECK MINIKUBE ----------------
Write-Host "🚀 Checking Minikube..." -ForegroundColor Cyan
$mkStatus = minikube status --format='{{.Host}}' 2>$null

if ($mkStatus -ne "Running") {
    Write-Host "➡️ Starting Minikube..." -ForegroundColor Yellow
    minikube start
} else {
    Write-Host "✅ Minikube already running" -ForegroundColor Green
}

# ---------------- NAMESPACE ----------------
kubectl get namespace idp > $null 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Host "📦 Creating namespace 'idp'"
    kubectl create namespace idp
} else {
    Write-Host "✅ Namespace 'idp' exists"
}

# ---------------- START BACKEND ----------------
Write-Host "🧠 Starting backend..."
Start-Process powershell -ArgumentList "cd '$root'; go run cmd/server/main.go"

# ---------------- START TUNNEL ----------------
Write-Host "🌐 Starting Minikube tunnel..."
Start-Process powershell -Verb RunAs -ArgumentList "minikube tunnel"

# ---------------- START CLOUDFLARE ----------------
Write-Host "☁️ Starting Cloudflare tunnel..."
Start-Process powershell -ArgumentList "cloudflared tunnel --url http://localhost:8080"

# ---------------- DONE ----------------
Write-Host ""
Write-Host "✅ Setup Complete!" -ForegroundColor Green
Write-Host ""
Write-Host "➡️ Wait for Cloudflare URL"
Write-Host "➡️ Add webhook:"
Write-Host "   https://<url>/webhook/github"
Write-Host ""
Write-Host "➡️ Open: http://localhost:8080"