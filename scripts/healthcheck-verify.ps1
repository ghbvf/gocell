$ErrorActionPreference = "Stop"

$Timeout = 30

Write-Host "Starting Docker Compose services..."
docker compose up -d --wait --timeout $Timeout

try {
  Write-Host ""
  Write-Host "All services healthy."
  Write-Host ""
  docker compose ps
} finally {
  Write-Host ""
  docker compose down
}

Write-Host "Healthcheck verification complete."
