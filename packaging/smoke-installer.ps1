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

# Use only a throwaway current-user test CA and a loopback HTTPS fixture.
# No employee account, subscription, proxy, or external model is contacted.
$fixtureExe = Join-Path $testRoot 'fixture.exe'
go build -o $fixtureExe ./packaging/testdata/gateway.go
if ($LASTEXITCODE -ne 0) { throw 'Failed to build local TLS fixture.' }
$fixture = Start-Process $fixtureExe -ArgumentList ('"' + $testRoot + '"') -PassThru
$authority = $null
$process = $null
$encoding = [Text.Encoding]::UTF8
function Wait-For($description, [scriptblock]$condition) {
    for ($i=0; $i -lt 160; $i++) {
        $value = & $condition
        if ($value) { return $value }
        Start-Sleep -Milliseconds 125
    }
    throw "Timed out waiting for $description"
}
try {
    $null = Wait-For 'test gateway' { Test-Path (Join-Path $testRoot 'fixture-origin') }
    $origin = Get-Content -Raw (Join-Path $testRoot 'fixture-origin')
    $authority = Import-Certificate -FilePath (Join-Path $testRoot 'test-ca.cer') -CertStoreLocation 'Cert:\CurrentUser\Root'
    $profile = @{ BaseURL = "$origin/v1"; Key = 'synthetic-ci-helper-key' }
    # Compile against Windows PowerShell's .NET Framework for System.Drawing.
    # Run this smoke in ordinary powershell.exe (set by the workflow).
    Add-Type -Path 'packaging/testdata/NativeWindowProbe.cs' -ReferencedAssemblies @('System.dll','System.Drawing.dll')
    $process = Start-Process $exe -PassThru
    $handle = Wait-For 'native Win32 setup window' {
        if ($process.HasExited) { throw 'Application exited before showing its window.' }
        $h = [KeyDockWindowProbe]::Window($process.Id,$null,'KeyDock Connect')
        if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::IsWindowVisible($h)) { $h }
    }
    $serverField = [KeyDockWindowProbe]::Child($handle,'server','')
    $keyField = [KeyDockWindowProbe]::Child($handle,'key','')
    [KeyDockWindowProbe]::Enter($serverField,$origin)
    [KeyDockWindowProbe]::Enter($keyField,'fixture-device-key')
    [KeyDockWindowProbe]::Click([KeyDockWindowProbe]::Child($handle,'button','连接并测试'))
    $null = Wait-For 'four native connection checks' {
        $checks = [KeyDockWindowProbe]::Text([KeyDockWindowProbe]::Child($handle,'checks',''))
        $checks -match '设备授权 · 完成' -and $checks -match 'Codex 配置 · 完成' -and
        $checks -match '模型连通 · 完成' -and $checks -match '代理连通 · 跳过'
    }
    $managed = [IO.File]::ReadAllBytes($config)
    if ($encoding.GetString($managed).Contains($profile.Key)) { throw 'The Codex profile contains a plaintext key.' }
    $shot = Join-Path (Resolve-Path dist/installer).Path 'KeyDockConnect-window.png'
    [KeyDockWindowProbe]::Capture($handle,$shot)
    $credential = (& $exe --credential | Out-String).Trim()
    if ($credential -cne $profile.Key -or $LASTEXITCODE -ne 0) { throw 'Installed credential helper failed.' }

    $runningAttempt = Start-Process $uninstaller -ArgumentList $flags -Wait -PassThru
    if ($runningAttempt.ExitCode -eq 0 -or -not (Test-Path $exe)) { throw 'Uninstall must stop while KeyDock Connect is running.' }
    [KeyDockWindowProbe]::HideViaClose($handle)
    $null = Wait-For 'hidden native window' { -not [KeyDockWindowProbe]::IsWindowVisible($handle) }
    $second = Start-Process $exe -PassThru -Wait
    if ($second.ExitCode -ne 0) { throw 'Second launch failed to signal the primary window.' }
    $null = Wait-For 'same primary HWND reopened by second launch' { [KeyDockWindowProbe]::IsWindowVisible($handle) }
    [KeyDockWindowProbe]::HideViaClose($handle)
    $null = Wait-For 'hidden native window before tray activation' { -not [KeyDockWindowProbe]::IsWindowVisible($handle) }
    $sink = [KeyDockWindowProbe]::Window($process.Id,'WalkNotifyIconSink',$null)
    # Exercise the pinned Walk version's real Shell_NotifyIcon callback and its menu.
    [KeyDockWindowProbe]::SelectTray($sink)
    $null = Wait-For 'visible native window after notification-area selection' { [KeyDockWindowProbe]::IsWindowVisible($handle) }
    [KeyDockWindowProbe]::OpenTrayMenu($sink)
    $popup = Wait-For 'tray context menu' {
        $h = [KeyDockWindowProbe]::Window($process.Id,'#32768',$null)
        if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::IsWindowVisible($h)) { $h }
    }
    if ([KeyDockWindowProbe]::MenuLabel($popup,0) -ne '打开 KeyDock Connect' -or [KeyDockWindowProbe]::MenuLabel($popup,1) -ne '退出接入助手') {
        throw 'The notification-area menu is missing its open or exit action.'
    }
    [KeyDockWindowProbe]::ExitViaMenu($popup)
    if (-not $process.WaitForExit(15000)) { throw 'The installed application did not exit through its notification-area menu.' }

    # Simulate a user edit outside the app. The uninstaller must retain the
    # credential executable and configuration until this conflict is resolved.
    $externalEdit = $encoding.GetString($managed).Replace($profile.BaseURL, 'https://other.example.invalid/v1')
    if ($externalEdit -ceq $encoding.GetString($managed)) { throw 'No managed base URL to edit.' }
    [IO.File]::WriteAllText($config, $externalEdit)
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
    Write-Host 'Verified native Windows setup, authenticated HTTPS device enrollment, model probe, credential helper, close-to-tray, reopening, tray actions and guarded uninstall.'
} finally {
    if ($null -ne $process -and -not $process.HasExited) { $process.Kill(); $process.WaitForExit() }
    if (-not $fixture.HasExited) { $fixture.Kill(); $fixture.WaitForExit() }
    if ($null -ne $authority) { Remove-Item ('Cert:\CurrentUser\Root\' + $authority.Thumbprint) }
}
