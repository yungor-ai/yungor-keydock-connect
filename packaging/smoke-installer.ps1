# This test installs a per-user program, exercises Windows DPAPI, and uninstalls it.
# Only run it in a disposable hosted Windows job.
$ErrorActionPreference = 'Stop'
if ($env:RUNNER_ENVIRONMENT -ne 'github-hosted' -or $env:RUNNER_OS -ne 'Windows') {
    throw 'This installation smoke test requires a disposable GitHub-hosted Windows runner.'
}

$installDir = Join-Path $env:LOCALAPPDATA 'Programs\KeyDock Connect'
if (Test-Path $installDir) { throw 'The install location must be empty on the disposable runner.' }
$installer = (Resolve-Path 'dist/installer/KeyDockConnect-Setup-windows-x64.exe').Path
$flags = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART')
$setup = Start-Process $installer -ArgumentList $flags -Wait -PassThru
if ($setup.ExitCode -ne 0) { throw "Installation failed ($($setup.ExitCode))." }

# Verify installed bytes, including all notices, source archives and routing data.
$stage = (Resolve-Path dist/app).Path
foreach ($file in (Get-ChildItem $stage -Recurse -File)) {
    $relative = [IO.Path]::GetRelativePath($stage, $file.FullName)
    $installed = Join-Path $installDir $relative
    if ((Get-FileHash $installed).Hash -ne (Get-FileHash $file.FullName).Hash) {
        throw "Installed file differs from the verified payload: $relative"
    }
}

$exe = Join-Path $installDir 'KeyDockConnect.exe'
$uninstaller = (Get-Item (Join-Path $installDir 'unins*.exe')).FullName
$testRoot = Join-Path $env:RUNNER_TEMP ('keydock-smoke-' + [Guid]::NewGuid().ToString('N'))
$env:APPDATA = Join-Path $testRoot 'AppData'
$env:CODEX_HOME = Join-Path $testRoot 'codex'
$state = Join-Path $env:APPDATA 'Yungor\KeyDockConnect'
$null = New-Item $state, $env:CODEX_HOME -ItemType Directory -Force
$config = Join-Path $env:CODEX_HOME 'config.toml'
$original = "# synthetic preexisting Codex settings`nmodel = `"previous-model`"`n"
[IO.File]::WriteAllText($config, $original)
$originalHash = (Get-FileHash $config).Hash

# Seed an already imported, synthetic profile using Windows current-user DPAPI.
# The bundle parser itself is exercised separately by the Go test suites.
$profile = @{
    DeviceName = 'Windows smoke test'; BaseURL = 'https://model.example.invalid/v1'
    Key = 'synthetic-ci-helper-key'; Model = 'synthetic-model'; SubscriptionURL = ''
}
$encoding = [Text.Encoding]::UTF8
$protection = [Security.Cryptography.DataProtectionScope]::CurrentUser
$plain = $encoding.GetBytes(($profile | ConvertTo-Json -Compress))
[IO.File]::WriteAllBytes((Join-Path $state 'device.bin'), [Security.Cryptography.ProtectedData]::Protect($plain, $null, $protection))

$process = Start-Process $exe -PassThru
try {
    $savedWindow = Join-Path $state 'window.bin'
    for ($i = 0; $i -lt 120 -and -not (Test-Path $savedWindow); $i++) {
        if ($process.HasExited) { throw 'Installed application exited before opening the desktop.' }
        Start-Sleep -Milliseconds 250
    }
    $window = [Uri]$encoding.GetString([Security.Cryptography.ProtectedData]::Unprotect([IO.File]::ReadAllBytes($savedWindow), $null, $protection))
    $origin = $window.GetLeftPart([UriPartial]::Authority)
    $headers = @{ Origin = $origin; 'X-KeyDock-Session' = $window.Fragment.Substring(1) }
    $page = Invoke-WebRequest "$origin/" -NoProxy
    if ($page.StatusCode -ne 200 -or $page.Content -notmatch 'KeyDock Connect') { throw 'Desktop page did not load.' }
    $result = Invoke-RestMethod "$origin/api/apply" -NoProxy -Method Post -Headers $headers -ContentType 'application/json' -Body '{"model":"synthetic-model"}'
    if (-not $result.configured) { throw 'Installed application could not apply the Codex profile.' }
    $managed = [IO.File]::ReadAllBytes($config)
    if ($encoding.GetString($managed).Contains($profile.Key)) { throw 'The Codex profile contains a plaintext key.' }

    # The installed GUI executable also serves as a noninteractive credential helper.
    $credential = (& $exe --credential | Out-String).Trim()
    if ($credential -cne $profile.Key -or $LASTEXITCODE -ne 0) { throw 'Installed credential helper did not return the test credential.' }
    $runningAttempt = Start-Process $uninstaller -ArgumentList $flags -Wait -PassThru
    if ($runningAttempt.ExitCode -eq 0 -or -not (Test-Path $exe)) { throw 'Uninstall must stop while KeyDock Connect is running.' }

    $null = Invoke-RestMethod "$origin/api/quit" -NoProxy -Method Post -Headers $headers
    if (-not $process.WaitForExit(15000)) { throw 'The installed application did not exit.' }

    # Simulate a user edit outside the app. The uninstaller must retain the
    # credential executable and configuration until this conflict is resolved.
    [IO.File]::AppendAllText($config, "# external edit`n")
    $modifiedHash = (Get-FileHash $config).Hash
    $conflict = Start-Process $uninstaller -ArgumentList $flags -Wait -PassThru
    if ($conflict.ExitCode -eq 0 -or -not (Test-Path $exe) -or (Get-FileHash $config).Hash -ne $modifiedHash) {
        throw 'Uninstall did not preserve the conflicting Codex configuration and helper.'
    }
    [IO.File]::WriteAllBytes($config, $managed)
    $uninstall = Start-Process $uninstaller -ArgumentList $flags -Wait -PassThru
    if ($uninstall.ExitCode -ne 0 -or (Test-Path $exe) -or (Get-FileHash $config).Hash -ne $originalHash) {
        throw 'Uninstall did not remove the program and restore the original Codex configuration.'
    }
    Write-Host 'Verified per-user installation, desktop startup, DPAPI credential helper, guarded uninstall and exact restoration.'
} finally {
    if (-not $process.HasExited) { $process.Kill(); $process.WaitForExit() }
}
