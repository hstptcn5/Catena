$ErrorActionPreference = "Stop"

$RootDir = Split-Path -Parent $PSScriptRoot
$ExampleDir = Join-Path $RootDir "examples\desktop-companion"
$DbPath = Join-Path $ExampleDir "desktop-companion.db"
$BinPath = Join-Path $RootDir "catena.exe"
$ApiKey = "pilot-dev-key"
$HostName = "127.0.0.1"
$Port = 8080
$BaseUrl = "http://$HostName`:$Port"
$ServerProcess = $null

function Stop-CatenaPilot {
    if ($null -ne $ServerProcess -and -not $ServerProcess.HasExited) {
        Stop-Process -Id $ServerProcess.Id -Force
        $ServerProcess.WaitForExit()
    }
}

try {
    Set-Location $RootDir

    try {
        Invoke-RestMethod "$BaseUrl/health" | Out-Null
        throw "A service is already responding at $BaseUrl. Stop it or change `$Port in this script before running the pilot."
    } catch {
        if ($_.Exception.Message -like "A service is already responding*") {
            throw
        }
    }

    Write-Host "Building Catena..."
    go build -o $BinPath .

    Write-Host "Seeding deterministic example database..."
    go run .\examples\desktop-companion\seed.go $DbPath

    Write-Host "Starting Catena for local pilot testing..."
    $Args = @(
        "serve",
        "--db", $DbPath,
        "--host", $HostName,
        "--port", "$Port",
        "--api-key", $ApiKey,
        "--max-rows", "100",
        "--backup-dir", (Join-Path $ExampleDir "backups")
    )
    $ServerProcess = Start-Process -FilePath $BinPath -ArgumentList $Args -PassThru -WindowStyle Hidden

    Write-Host "Waiting for health readiness..."
    $Ready = $false
    for ($i = 0; $i -lt 20; $i++) {
        try {
            Invoke-RestMethod "$BaseUrl/health" | Out-Null
            $Ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    if (-not $Ready) {
        throw "Catena did not become ready at $BaseUrl"
    }

    Write-Host "Running authenticated query..."
    Invoke-RestMethod "$BaseUrl/query" `
        -Method Post `
        -Headers @{ Authorization = "Bearer $ApiKey" } `
        -ContentType "application/json" `
        -Body '{"sql":"SELECT sku, name, quantity FROM inventory ORDER BY sku","params":[]}' |
        ConvertTo-Json -Depth 10

    Write-Host ""
    Write-Host "Catena pilot is running."
    Write-Host ""
    Write-Host "Admin UI:"
    Write-Host "  $BaseUrl"
    Write-Host ""
    Write-Host "Use this development API key only for the local pilot:"
    Write-Host "  $ApiKey"
    Write-Host ""
    Write-Host "Try these next commands in another terminal:"
    Write-Host ""
    Write-Host "Invoke-RestMethod $BaseUrl/query ``"
    Write-Host "  -Method Post ``"
    Write-Host "  -Headers @{ Authorization = `"Bearer $ApiKey`" } ``"
    Write-Host "  -ContentType `"application/json`" ``"
    Write-Host "  -Body '{`"sql`":`"UPDATE inventory SET quantity = quantity + ? WHERE sku = ?`",`"params`":[2,`"CAT-001`"]}'"
    Write-Host ""
    Write-Host "Invoke-WebRequest $BaseUrl/export ``"
    Write-Host "  -Headers @{ Authorization = `"Bearer $ApiKey`" } ``"
    Write-Host "  -OutFile desktop-companion-export.db"
    Write-Host ""
    Write-Host "Press Ctrl+C here to stop Catena and clean up the server process."

    while (-not $ServerProcess.HasExited) {
        Start-Sleep -Seconds 1
    }
} finally {
    Stop-CatenaPilot
}
