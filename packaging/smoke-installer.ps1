# This test installs a per-user program, exercises Windows DPAPI, and uninstalls it.
# Only run it in a disposable hosted Windows job.
$ErrorActionPreference = 'Stop'
if ($env:RUNNER_ENVIRONMENT -ne 'github-hosted' -or $env:RUNNER_OS -ne 'Windows') {
    throw 'This installation smoke test requires a disposable GitHub-hosted Windows runner.'
}

$evidence = Join-Path (Resolve-Path dist/installer).Path 'smoke-diagnostics.txt'
function Trace-Smoke([string]$message) {
    $line = '{0:u} {1}' -f [datetime]::UtcNow, $message
    Write-Host $line
    Add-Content -Encoding UTF8 -LiteralPath $evidence -Value $line
}
function Invoke-SmokeProcess([string]$label, [string]$file, [string[]]$arguments, [int]$seconds) {
    Trace-Smoke "Starting $label"
    $start = New-Object Diagnostics.ProcessStartInfo
    $start.FileName = $file
    $start.Arguments = [string]::Join(' ', $arguments)
    $start.UseShellExecute = $false
    $p = [Diagnostics.Process]::Start($start)
    if (-not $p.WaitForExit($seconds * 1000)) {
        Trace-Smoke "Timeout in $label (PID $($p.Id))"
        & taskkill.exe /PID $p.Id /T /F | Out-Null
        throw "$label did not exit within $seconds seconds"
    }
    Trace-Smoke "$label exited with $($p.ExitCode)"
    return $p.ExitCode
}
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\KeyDock Connect'
if (Test-Path $installDir) { throw 'The install location must be empty on the disposable runner.' }
$installer = (Resolve-Path 'dist/installer/KeyDockConnect-Setup-windows-x64.exe').Path
$flags = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART')
$setupCode = Invoke-SmokeProcess 'per-user installation' $installer $flags 90
if ($setupCode -ne 0) { throw "Installation failed ($setupCode)." }
Trace-Smoke 'Checking installed files against the staged payload'

# Verify installed bytes, including all notices, source archives and routing data.
$stage = (Resolve-Path dist/app).Path
foreach ($file in (Get-ChildItem $stage -Recurse -File)) {
    $relative = $file.FullName.Substring($stage.Length + 1)
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

# The disposable runner trusts one temporary CA in its machine store. The
# current-user root store can request interactive approval on the desktop.
# No employee account, subscription, proxy, or external model is contacted.
$fixtureExe = Join-Path $testRoot 'fixture.exe'
Trace-Smoke 'Building synthetic loopback HTTPS fixture'
go build -o $fixtureExe ./packaging/testdata/gateway.go
if ($LASTEXITCODE -ne 0) { throw 'Failed to build local TLS fixture.' }
$fixture = Start-Process $fixtureExe -ArgumentList ('"' + $testRoot + '"') -PassThru
$authority = $null
$process = $null
$encoding = [Text.Encoding]::UTF8
function Wait-For($description, [scriptblock]$condition) {
    Trace-Smoke "Waiting for $description"
    $clock = [Diagnostics.Stopwatch]::StartNew()
    while ($clock.Elapsed.TotalSeconds -lt 20) {
        $value = & $condition
        if ($value) { Trace-Smoke "Observed $description"; return $value }
        Start-Sleep -Milliseconds 125
    }
    Trace-Smoke "Timed out waiting for $description"
    throw "Timed out waiting for $description"
}
try {
    $null = Wait-For 'test gateway' { Test-Path (Join-Path $testRoot 'fixture-origin') }
    $origin = Get-Content -Raw (Join-Path $testRoot 'fixture-origin')
    $caPath = Join-Path $testRoot 'test-ca.cer'
    $authority = [Security.Cryptography.X509Certificates.X509Certificate2]::new([string]$caPath)
    $certCode = Invoke-SmokeProcess 'trust disposable HTTPS fixture CA' 'certutil.exe' @('-f','-addstore','Root',('"' + $caPath + '"')) 20
    if ($certCode -ne 0) { throw 'Could not install the disposable fixture CA.' }
    $profile = @{ BaseURL = "$origin/v1"; Key = 'synthetic-ci-helper-key' }
    # Compile against Windows PowerShell's .NET Framework for System.Drawing.
    # Run this smoke in ordinary powershell.exe (set by the workflow).
    Trace-Smoke 'Compiling Win32 window probe'
    Add-Type -Path 'packaging/testdata/NativeWindowProbe.cs' -ReferencedAssemblies @('System.dll','System.Drawing.dll')
    Trace-Smoke 'Starting installed native application'
    $nativeStderr = Join-Path $testRoot 'native.stderr'
    $process = Start-Process $exe -PassThru -RedirectStandardError $nativeStderr
    Trace-Smoke "Started native PID $($process.Id)"
    $handle = Wait-For 'native Win32 setup window' {
        if ($process.HasExited) { throw 'Application exited before showing its window.' }
        $h = [KeyDockWindowProbe]::Window($process.Id,$null,'KeyDock Connect')
        if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::IsWindowVisible($h)) { $h }
    }
    $serverField = [KeyDockWindowProbe]::Child($handle,'server','')
    $keyField = [KeyDockWindowProbe]::Child($handle,'key','')
    $shots = (New-Item -ItemType Directory -Force -Path 'dist/installer/screenshots').FullName
    $startButton = Wait-For 'initial action' {
        $h = [KeyDockWindowProbe]::Child($handle,'button','连接并测试')
        if ($h -ne [IntPtr]::Zero) { $h }
    }
    $null = Wait-For 'initial connection status' {
        [KeyDockWindowProbe]::Text([KeyDockWindowProbe]::Child($handle,'checks','')) -match '设备授权 · 待检查'
    }
    [KeyDockWindowProbe]::Capture($handle,(Join-Path $shots '01-first-launch.png'),'设备接入')
    [KeyDockWindowProbe]::Enter($serverField,$origin)
    [KeyDockWindowProbe]::Enter($keyField,'intentionally-wrong-synthetic-key')
    [KeyDockWindowProbe]::Click($startButton)
    $null = Wait-For 'rejected device credential and enabled retry' {
        $checks = [KeyDockWindowProbe]::Text([KeyDockWindowProbe]::Child($handle,'checks',''))
        $retry = [KeyDockWindowProbe]::Child($handle,'button','连接并测试')
        $checks -match '设备授权 · 失败' -and $checks -match '401' -and $retry -ne [IntPtr]::Zero
    }
    [KeyDockWindowProbe]::Capture($handle,(Join-Path $shots '02-device-key-error.png'),'设备接入')
    [KeyDockWindowProbe]::Enter($keyField,'fixture-device-key')
    [KeyDockWindowProbe]::Click($startButton)
    $null = Wait-For 'four native connection checks' {
        $checks = [KeyDockWindowProbe]::Text([KeyDockWindowProbe]::Child($handle,'checks',''))
        $ready = [KeyDockWindowProbe]::Child($handle,'button','打开 Codex')
        $checks -match '设备授权 · 完成' -and $checks -match 'Codex 配置 · 完成' -and
        $checks -match '模型连通 · 完成' -and $checks -match '代理连通 · 跳过' -and $ready -ne [IntPtr]::Zero
    }
    $managed = [IO.File]::ReadAllBytes($config)
    if ($encoding.GetString($managed).Contains($profile.Key)) { throw 'The Codex profile contains a plaintext key.' }
    $shot = Join-Path (Resolve-Path dist/installer).Path 'KeyDockConnect-window.png'
    $connectedShot = Join-Path $shots '03-connected.png'
    [KeyDockWindowProbe]::Capture($handle,$connectedShot,'Windows smoke test · synthetic-model')
    Copy-Item $connectedShot $shot -Force
    Trace-Smoke 'Checking installed DPAPI credential helper'
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $exe
    $info.Arguments = '--credential'
    $info.UseShellExecute = $false
    $info.RedirectStandardOutput = $true
    $helper = [Diagnostics.Process]::Start($info)
    if (-not $helper.WaitForExit(8000)) { $helper.Kill(); throw 'Credential helper did not exit' }
    if ($helper.ExitCode -ne 0 -or $helper.StandardOutput.ReadToEnd().Trim() -cne $profile.Key) { throw 'Installed credential helper failed.' }
    $helper.Dispose()

    $runningAttempt = Invoke-SmokeProcess 'uninstall while app is open' $uninstaller $flags 35
    if ($runningAttempt -eq 0 -or -not (Test-Path $exe)) { throw 'Uninstall must stop while KeyDock Connect is running.' }
    [KeyDockWindowProbe]::HideViaClose($handle)
    $null = Wait-For 'hidden native window' { -not [KeyDockWindowProbe]::IsWindowVisible($handle) }
    if ($process.HasExited) { throw "Primary exited after hide (exit $($process.ExitCode))" }
    Trace-Smoke 'Testing second launch'
    $second = Start-Process $exe -PassThru
    if (-not $second.WaitForExit(12000)) { $second.Kill(); throw 'Second launch timed out' }
    if ($second.ExitCode -ne 0) { throw 'Second launch failed to signal the primary window.' }
    $null = Wait-For 'same primary HWND reopened by second launch' { [KeyDockWindowProbe]::IsWindowVisible($handle) }
    [KeyDockWindowProbe]::HideViaClose($handle)
    $null = Wait-For 'hidden native window before tray activation' { -not [KeyDockWindowProbe]::IsWindowVisible($handle) }
    $sink = [KeyDockWindowProbe]::Window($process.Id,'WalkNotifyIconSink',$null)
    # Exercise the pinned Walk version's real Shell_NotifyIcon callback and its menu.
    [KeyDockWindowProbe]::SelectTray($sink)
    $null = Wait-For 'visible native window after notification-area selection' { [KeyDockWindowProbe]::IsWindowVisible($handle) }
    Trace-Smoke ([KeyDockWindowProbe]::DesktopInfo($sink))
    [KeyDockWindowProbe]::OpenTrayMenu($sink)
    Start-Sleep -Milliseconds 250
    Trace-Smoke ([KeyDockWindowProbe]::DesktopInfo($sink))
    $popup = Wait-For 'tray context menu' {
        $h = [KeyDockWindowProbe]::TrayPopup($sink)
        if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::IsWindowVisible($h)) { $h }
    }
    if ([KeyDockWindowProbe]::MenuLabel($popup,0) -ne '打开 KeyDock Connect' -or [KeyDockWindowProbe]::MenuLabel($popup,1) -ne '退出接入助手') {
        throw 'The notification-area menu is missing its open or exit action.'
    }
    [KeyDockWindowProbe]::CaptureTray($popup,$sink,(Join-Path $shots '04-tray-menu.png'))
    [KeyDockWindowProbe]::ExitViaMenu($popup)
    if (-not $process.WaitForExit(15000)) { throw 'The installed application did not exit through its notification-area menu.' }
    if ($process.ExitCode -ne 0) { throw "Tray exit failed (exit $($process.ExitCode))" }

    # Simulate a user edit outside the app. The uninstaller must retain the
    # credential executable and configuration until this conflict is resolved.
    $externalEdit = $encoding.GetString($managed).Replace($profile.BaseURL, 'https://other.example.invalid/v1')
    if ($externalEdit -ceq $encoding.GetString($managed)) { throw 'No managed base URL to edit.' }
    [IO.File]::WriteAllText($config, $externalEdit)
    $modifiedHash = (Get-FileHash $config).Hash
    $conflict = Invoke-SmokeProcess 'uninstall with a conflicting edit' $uninstaller $flags 35
    if ($conflict -eq 0 -or -not (Test-Path $exe) -or (Get-FileHash $config).Hash -ne $modifiedHash) {
        throw 'Uninstall did not preserve the conflicting Codex configuration and helper.'
    }
    [IO.File]::WriteAllBytes($config, $managed)
    $uninstall = Invoke-SmokeProcess 'clean uninstall' $uninstaller $flags 45
    if ($uninstall -ne 0 -or (Test-Path $exe) -or (Get-FileHash $config).Hash -ne $originalHash) {
        throw 'Uninstall did not remove the program and restore the original Codex configuration.'
    }
    Write-Host 'Verified native Windows setup, authenticated HTTPS device enrollment, model probe, credential helper, close-to-tray, reopening, tray actions and guarded uninstall.'
} catch {
    Trace-Smoke ('Failure: ' + $_.Exception.Message)
    if ($null -ne $process) {
        $process.Refresh()
        if ($process.HasExited) { Trace-Smoke "Native PID $($process.Id) exited with $($process.ExitCode)" }
        else { Trace-Smoke "Native PID $($process.Id), main HWND $($process.MainWindowHandle)" }
    }
    throw
} finally {
    if ($null -ne $process -and -not $process.HasExited) { $process.Kill(); $null = $process.WaitForExit(5000) }
    if ($nativeStderr -and (Test-Path -LiteralPath $nativeStderr)) {
        $crash = [IO.File]::ReadAllText($nativeStderr)
        foreach ($sensitive in @('fixture-device-key','intentionally-wrong-synthetic-key','synthetic-ci-helper-key')) {
            $crash = $crash.Replace($sensitive,'[REDACTED]')
        }
        if ($crash) { Add-Content -Encoding UTF8 -LiteralPath $evidence -Value ("Native stderr:`n" + $crash) }
    }
    if (-not $fixture.HasExited) { $fixture.Kill(); $null = $fixture.WaitForExit(5000) }
    if ($null -ne $authority) {
        $path = 'Cert:\LocalMachine\Root\' + $authority.Thumbprint
        if (Test-Path -LiteralPath $path) {
            $removeCode = Invoke-SmokeProcess 'remove disposable HTTPS fixture CA' 'certutil.exe' @('-delstore','Root',$authority.Thumbprint) 15
            if ($removeCode -ne 0) { throw 'Could not remove the disposable fixture CA' }
        }
        $authority.Dispose()
    }
}
