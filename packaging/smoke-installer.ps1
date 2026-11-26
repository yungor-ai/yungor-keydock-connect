# This test installs KeyDock Connect in Program Files, exercises Windows DPAPI, and uninstalls it.
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
$installDir = Join-Path $env:ProgramFiles 'KeyDock Connect'
if (Test-Path $installDir) { throw 'The install location must be empty on the disposable runner.' }
$installer = (Resolve-Path 'dist/installer/KeyDockConnect-Setup-windows-x64.exe').Path
$version = (Get-Content -Raw -LiteralPath 'VERSION').Trim()
$versionInfo = (Get-Item -LiteralPath $installer).VersionInfo
if ("$($versionInfo.FileMajorPart).$($versionInfo.FileMinorPart).$($versionInfo.FileBuildPart)" -ne $version) {
    throw 'The Windows installer file version differs from VERSION.'
}
$flags = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART')
$legacyRegistration = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\{1F72CD21-C12E-40B7-9D93-98527EF65242}_is1'
if (Test-Path -LiteralPath $legacyRegistration) { throw 'The disposable runner already has a legacy install registration.' }
$null = New-Item -Path $legacyRegistration -Force
try {
    $null = New-ItemProperty -LiteralPath $legacyRegistration -Name DisplayName -Value 'KeyDock Connect'
    $setupCode = Invoke-SmokeProcess 'installation with legacy per-user registration' $installer $flags 30
    # Inno exits with 7 when PrepareToInstall refuses to proceed.
    if ($setupCode -ne 7 -or (Test-Path -LiteralPath $installDir)) { throw 'Setup did not stop for the legacy per-user install.' }
} finally {
    Remove-Item -LiteralPath $legacyRegistration
}
$customDir = Join-Path $env:RUNNER_TEMP ('keydock-custom-dir-' + [Guid]::NewGuid().ToString('N'))
$setupCode = Invoke-SmokeProcess 'installation with redirected directory' $installer ($flags + ('/DIR="' + $customDir + '"')) 30
if ($setupCode -ne 7 -or (Test-Path -LiteralPath $installDir) -or (Test-Path -LiteralPath $customDir)) {
    throw 'Setup accepted a directory outside Program Files.'
}
$setupCode = Invoke-SmokeProcess 'Program Files installation' $installer $flags 90
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
# All credentials, the SS peer, and the model service are local fixtures. The
# installed application also performs its normal public HTTPS 204 probe.
$fixtureExe = Join-Path $testRoot 'fixture.exe'
Trace-Smoke 'Building synthetic loopback HTTPS fixture'
go build -o $fixtureExe ./packaging/testdata/gateway.go
if ($LASTEXITCODE -ne 0) { throw 'Failed to build local TLS fixture.' }
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$listener.Start()
$ssPort = $listener.LocalEndpoint.Port
$listener.Stop()
$ssConfig = Join-Path $testRoot 'loopback-ss.yaml'
[IO.File]::WriteAllText($ssConfig, @"
mode: direct
log-level: silent
listeners:
  - name: fixture-ss
    type: shadowsocks
    udp: false
    listen: 127.0.0.1
    port: $ssPort
    cipher: 2022-blake3-aes-128-gcm
    password: "AAAAAAAAAAAAAAAAAAAAAA=="
    users: {test-device: "AQEBAQEBAQEBAQEBAQEBAQ=="}
"@)
$fixture = Start-Process $fixtureExe -ArgumentList ('"{0}" {1}' -f $testRoot,$ssPort) -PassThru
$peer = $null
$authority = $null
$process = $null
$exitHandle = $null
$encoding = [Text.Encoding]::UTF8
function Wait-For($description, [scriptblock]$condition, [int]$seconds=20) {
    Trace-Smoke "Waiting for $description"
    $clock = [Diagnostics.Stopwatch]::StartNew()
    while ($clock.Elapsed.TotalSeconds -lt $seconds) {
        $value = & $condition
        if ($value) { Trace-Smoke "Observed $description"; return $value }
        Start-Sleep -Milliseconds 125
    }
    Trace-Smoke "Timed out waiting for $description"
    throw "Timed out waiting for $description"
}
function Get-ProxyActionCount {
    $history = Get-Content -Raw -LiteralPath (Join-Path $state 'diagnostics-history.json') | ConvertFrom-Json
    return @($history | Where-Object { $_.stage -in @('proxy.enable','proxy.disable') }).Count
}
function Test-CrossPathTrayIdentity {
    if (-not [KeyDockWindowProbe]::HasGuidTray($initialSink)) {
        throw 'The installed instance must own its tray icon before testing a different executable path.'
    }
    # Separate synthetic state permits a second process from the staged path.
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = Join-Path $stage 'KeyDockConnect.exe'
    $info.UseShellExecute = $false
    $info.EnvironmentVariables['APPDATA'] = Join-Path $testRoot 'tray-second-appdata'
    $info.EnvironmentVariables['CODEX_HOME'] = Join-Path $testRoot 'tray-second-codex'
    $alternateState = Join-Path $info.EnvironmentVariables['APPDATA'] 'Yungor\KeyDockConnect'
    $null = New-Item -ItemType Directory -Path $alternateState -Force
    Copy-Item -LiteralPath (Join-Path $state 'device.bin') -Destination $alternateState
    $alternate = [Diagnostics.Process]::Start($info)
    try {
        $alternateWindow = Wait-For 'secondary native window at a different executable path' {
            if ($alternate.HasExited -or [KeyDockWindowProbe]::Window($alternate.Id,'#32770','KeyDock 接入助手') -ne [IntPtr]::Zero) {
                throw 'The secondary instance failed during startup.'
            }
            $h = [KeyDockWindowProbe]::Window($alternate.Id,$null,"KeyDock Connect v$version")
            if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::IsWindowVisible($h)) { $h }
        }
        $alternateSink = Wait-For 'secondary notification icon registered for its executable path' {
            $h = [KeyDockWindowProbe]::Window($alternate.Id,'WalkNotifyIconSink',$null)
            if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::HasGuidTray($h)) { $h }
        }
        if ([KeyDockWindowProbe]::TrayGuid($alternateSink) -eq [KeyDockWindowProbe]::TrayGuid($initialSink) -or
            -not [KeyDockWindowProbe]::HasGuidTray($initialSink)) {
            throw 'The two executable paths do not have separate registered tray icons.'
        }
        [KeyDockWindowProbe]::HideViaClose($alternateWindow)
        $null = Wait-For 'hidden secondary window after close' { -not [KeyDockWindowProbe]::IsWindowVisible($alternateWindow) }
        [KeyDockWindowProbe]::SelectTray($alternateSink)
        $null = Wait-For 'secondary window reopened through its tray' { [KeyDockWindowProbe]::IsWindowVisible($alternateWindow) }
        [KeyDockWindowProbe]::SelectMode($alternateWindow,1)
        [KeyDockWindowProbe]::Click([KeyDockWindowProbe]::Child($alternateWindow,'button','启用 TUN'))
        # This staged executable deliberately fails TUN preflight. The
        # download instructions wrap in the same label as the admin instructions.
        $tunPreflight = '请从 KeyDock 的「软件下载」页下载 Windows 安装包，退出托盘中的助手后安装，再从开始菜单打开 KeyDock Connect'
        $null = Wait-For 'TUN preflight guidance fits across multiple lines' {
            [KeyDockWindowProbe]::HasReadableWrappedLabel($alternateWindow,$tunPreflight)
        }
        [KeyDockWindowProbe]::Click([KeyDockWindowProbe]::Child($alternateWindow,'button','退出接入助手'))
        if (-not $alternate.WaitForExit(15000) -or $alternate.ExitCode -ne 0) {
            throw 'The secondary instance failed to exit cleanly.'
        }
    } finally {
        if (-not $alternate.HasExited) { $alternate.Kill(); $null = $alternate.WaitForExit(5000) }
        $alternate.Dispose()
    }
    if ($process.HasExited -or -not [KeyDockWindowProbe]::HasGuidTray($initialSink)) {
        throw 'The secondary instance interrupted the installed instance or its GUID tray.'
    }
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
    Add-Type -Path 'packaging/testdata/NativeWindowProbe.cs' -ReferencedAssemblies @('System.dll','System.Drawing.dll','System.Windows.Forms.dll')
    Trace-Smoke 'Starting installed native application'
    $nativeStderr = Join-Path $testRoot 'native.stderr'
    $process = Start-Process $exe -PassThru -RedirectStandardError $nativeStderr
    $exitHandle = [KeyDockWindowProbe]::WatchExit($process.Id)
    Trace-Smoke "Started native PID $($process.Id)"
    $handle = Wait-For 'native Win32 setup window' {
        if ($process.HasExited) { throw 'Application exited before showing its window.' }
        $h = [KeyDockWindowProbe]::Window($process.Id,$null,"KeyDock Connect v$version")
        if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::IsWindowVisible($h)) { $h }
    }
    $initialSink = [KeyDockWindowProbe]::Window($process.Id,'WalkNotifyIconSink',$null)
    Trace-Smoke ([KeyDockWindowProbe]::DesktopInfo($initialSink))
    $serverField = [KeyDockWindowProbe]::Child($handle,'server','')
    $keyField = [KeyDockWindowProbe]::Child($handle,'key','')
    $shots = (New-Item -ItemType Directory -Force -Path 'dist/installer/screenshots').FullName
    $startButton = Wait-For 'initial action' {
        $h = [KeyDockWindowProbe]::Child($handle,'button','连接并测试')
        if ($h -ne [IntPtr]::Zero) { $h }
    }
    $null = Wait-For 'initial connection status' {
        [KeyDockWindowProbe]::CheckStatus($handle,'设备授权','待检查',$null) -and
        [KeyDockWindowProbe]::Child($handle,'label','系统代理未启用') -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label-prefix','最近更新时间 ') -eq [IntPtr]::Zero
    }
    if ([KeyDockWindowProbe]::Child($handle,'any-button','打开 Codex') -ne [IntPtr]::Zero) {
        throw 'The native window still contains the Open Codex button.'
    }
    [KeyDockWindowProbe]::Capture($handle,(Join-Path $shots '01-first-launch.png'),'设备接入')
    [KeyDockWindowProbe]::SelectTray($initialSink)
    [KeyDockWindowProbe]::Enter($serverField,$origin)
    [KeyDockWindowProbe]::Click([KeyDockWindowProbe]::Child($handle,'button','检查更新'))
    $updateDialog = Wait-For 'update confirmation using the entered server without a Device Key' {
        $h = [KeyDockWindowProbe]::Window($process.Id,$null,'KeyDock Connect 更新')
        if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::Child($h,'label','发现新版本 v99.0.0') -ne [IntPtr]::Zero) { $h }
    }
    [KeyDockWindowProbe]::Click([KeyDockWindowProbe]::Child($updateDialog,'button','取消'))
    $null = Wait-For 'update cancelled with main window still available' {
        [KeyDockWindowProbe]::Child($handle,'label','已取消更新。') -ne [IntPtr]::Zero
    }
    if (Test-Path (Join-Path $testRoot 'unexpected-update-download')) { throw 'Cancelling an update started a download.' }
    [KeyDockWindowProbe]::Enter($keyField,'intentionally-wrong-synthetic-key')
    [KeyDockWindowProbe]::Click($startButton)
    $null = Wait-For 'rejected device credential and enabled retry' {
        $retry = [KeyDockWindowProbe]::Child($handle,'button','连接并测试')
        [KeyDockWindowProbe]::CheckStatus($handle,'设备授权','失败','401') -and $retry -ne [IntPtr]::Zero
    }
    [KeyDockWindowProbe]::Capture($handle,(Join-Path $shots '02-device-key-error.png'),'设备接入')
    $diagnosticsPath = Join-Path $testRoot 'connection-diagnostics.json'
    [KeyDockWindowProbe]::ClickForModal([KeyDockWindowProbe]::Child($handle,'button','导出诊断日志'))
    $saveDialog = Wait-For 'native diagnostics save dialog' {
        $h = [KeyDockWindowProbe]::Window($process.Id,'#32770','导出诊断日志')
        if ($h -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::IsWindowVisible($h)) { $h }
    }
    $null = Wait-For 'diagnostics filename field' { [KeyDockWindowProbe]::SetDiagnosticFileName($saveDialog,$diagnosticsPath) }
    try {
        Trace-Smoke ([KeyDockWindowProbe]::AcceptFileDialog($handle,$saveDialog))
        $null = Wait-For 'closed diagnostics save dialog' { -not [KeyDockWindowProbe]::IsWindowVisible($saveDialog) }
        $null = Wait-For 'diagnostic file and restored controls' {
            (Test-Path -LiteralPath $diagnosticsPath) -and [KeyDockWindowProbe]::Child($handle,'button','连接并测试') -ne [IntPtr]::Zero
        }
    } catch {
        Trace-Smoke ([KeyDockWindowProbe]::VisibleDialogs($process.Id))
        $saveFailed = [KeyDockWindowProbe]::Child($handle,'label-prefix','无法保存诊断日志') -ne [IntPtr]::Zero
        $saveReported = [KeyDockWindowProbe]::Child($handle,'label-prefix','诊断日志已保存') -ne [IntPtr]::Zero
        Trace-Smoke "Diagnostic export evidence: expected file exists=$(Test-Path -LiteralPath $diagnosticsPath), save failed=$saveFailed, save reported=$saveReported"
        throw
    }
    $diagnosticJSON = Get-Content -Raw -Encoding UTF8 -LiteralPath $diagnosticsPath
    $diagnostics = $diagnosticJSON | ConvertFrom-Json
    $rejectedAccess = @($diagnostics.events | Where-Object {
        $_.stage -eq 'device' -and $_.outcome -eq 'failed' -and $_.http_status -eq 401
    })
    if ($diagnostics.schema_version -ne 1 -or $diagnostics.app_version -ne $version -or $rejectedAccess.Count -eq 0) {
        throw 'The diagnostic export did not include the rejected device check.'
    }
    if ($diagnosticJSON.Contains('intentionally-wrong-synthetic-key') -or $diagnosticJSON.Contains($origin.Trim())) {
        throw 'The diagnostic export contains the raw device key or gateway origin.'
    }
    [KeyDockWindowProbe]::Enter($keyField,'fixture-device-key')
    [KeyDockWindowProbe]::Click($startButton)
    $null = Wait-For 'four native connection checks' {
        $ready = [KeyDockWindowProbe]::Child($handle,'button','恢复原 Codex 配置')
        [KeyDockWindowProbe]::CheckStatus($handle,'设备授权','完成','演示工作站') -and
        [KeyDockWindowProbe]::CheckStatus($handle,'Codex 配置','完成','synthetic-model') -and
        [KeyDockWindowProbe]::CheckStatus($handle,'模型连通','完成',$null) -and
        [KeyDockWindowProbe]::CheckStatus($handle,'代理连通','跳过','未分配') -and
        [KeyDockWindowProbe]::Child($handle,'label','演示工作站 · synthetic-model') -ne [IntPtr]::Zero -and
        $ready -ne [IntPtr]::Zero
    }
    [KeyDockWindowProbe]::Capture($handle,(Join-Path $shots '03-without-subscription.png'),'设备接入')
    [KeyDockWindowProbe]::AssertAlignedChecks($handle)

    # Give the same paired device a loopback SS2022 node. Resync with the
    # saved Device Key and simulate clock drift without changing Windows time.
    $null = New-Item -ItemType File -Path (Join-Path $testRoot 'subscription-enabled')
    $skewMarker = Join-Path $testRoot 'proxy-clock-skew-failure'
    $null = New-Item -ItemType File -Path $skewMarker
    $clockHint = '检测到本机时钟比 KeyDock 服务端慢了 30 秒以上。请在 Windows 时间设置中点「立即同步」，再复测。'
    $tunHint = '若 Clash 等其他代理工具开启了 TUN（虚拟网卡），可在对应工具中关闭 TUN，再点「连接并测试」。'
    try {
        [KeyDockWindowProbe]::Click($startButton)
        $null = Wait-For 'visible clock and TUN guidance after a proxy failure' {
            $timeSettings = [KeyDockWindowProbe]::Child($handle,'button','打开时间设置')
            [KeyDockWindowProbe]::CheckStatus($handle,'设备授权','完成','演示工作站') -and
            [KeyDockWindowProbe]::CheckStatus($handle,'Codex 配置','完成','synthetic-model') -and
            [KeyDockWindowProbe]::CheckStatus($handle,'模型连通','完成',$null) -and
            [KeyDockWindowProbe]::CheckStatus($handle,'代理连通','失败','HTTP 503') -and
            [KeyDockWindowProbe]::Child($handle,'label','代理故障排查') -ne [IntPtr]::Zero -and
            [KeyDockWindowProbe]::Child($handle,'label',$clockHint) -ne [IntPtr]::Zero -and
            [KeyDockWindowProbe]::Child($handle,'label',$tunHint) -ne [IntPtr]::Zero -and
            [KeyDockWindowProbe]::IsWindowVisible($timeSettings)
        }
        [KeyDockWindowProbe]::Capture($handle,(Join-Path $shots '06-proxy-troubleshooting.png'),'设备接入')
    } finally {
        Remove-Item -LiteralPath $skewMarker
    }

    # Restore the normal subscription and exercise the system-proxy actions.
    # Keep the synthetic SS peer outside the installation tree during upgrades.
    $peer = Start-Process (Join-Path $stage 'core\mihomo.exe') -ArgumentList ('-d "{0}" -f "{1}"' -f $testRoot,$ssConfig) -PassThru
    $null = Wait-For 'local SS2022 fixture listener' {
        if ($peer.HasExited) { throw 'Loopback SS peer exited early.' }
        $client = [Net.Sockets.TcpClient]::new()
        try { $client.Connect('127.0.0.1',$ssPort); $true } catch { $false } finally { $client.Dispose() }
    }
    [KeyDockWindowProbe]::Click($startButton)
    $null = Wait-For 'assigned SS subscription tested without enabling Windows proxy' {
        $canEnable = [KeyDockWindowProbe]::Child($handle,'button','启用系统代理')
        $off = [KeyDockWindowProbe]::Child($handle,'label','系统代理未启用')
        $done = [KeyDockWindowProbe]::CheckStatus($handle,'代理连通','完成',$null)
        $offline = [KeyDockWindowProbe]::CheckStatus($handle,'代理连通','失败',$null)
        ($done -or $offline) -and $canEnable -ne [IntPtr]::Zero -and $off -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label',$clockHint) -eq [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label','最近更新时间 尚未更新') -ne [IntPtr]::Zero
    } 55
    Trace-Smoke ("Public 204 probe through local SS peer succeeded: " + [KeyDockWindowProbe]::CheckStatus($handle,'代理连通','完成',$null))
    $managed = [IO.File]::ReadAllBytes($config)
    $managedHash = (Get-FileHash $config).Hash
    if ($encoding.GetString($managed).Contains($profile.Key)) { throw 'The Codex profile contains a plaintext key.' }
    $shot = Join-Path (Resolve-Path dist/installer).Path 'KeyDockConnect-window.png'
    $connectedShot = Join-Path $shots '03-connected.png'
    [KeyDockWindowProbe]::Capture($handle,$connectedShot,'设备接入')
    [KeyDockWindowProbe]::AssertReadableLabel($handle,'演示工作站 · synthetic-model')
    [KeyDockWindowProbe]::AssertReadableLabel($handle,'启用后，使用系统代理的应用流量将按订阅规则分流。')
    Copy-Item $connectedShot $shot -Force
    Test-CrossPathTrayIdentity

    $internetSettings = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'
    $beforeSelection = Get-ItemProperty $internetSettings
    $actionsBeforeSelection = Get-ProxyActionCount
    [KeyDockWindowProbe]::SelectMode($handle,1)
    $null = Wait-For 'TUN selected and awaiting confirmation' {
        [KeyDockWindowProbe]::Child($handle,'button','启用 TUN') -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label','TUN 未启用') -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label','通过虚拟网卡按订阅规则分流，覆盖绕过系统代理的应用。') -ne [IntPtr]::Zero
    }
    [KeyDockWindowProbe]::SelectMode($handle,0)
    $null = Wait-For 'system proxy selected and awaiting confirmation' {
        [KeyDockWindowProbe]::Child($handle,'button','启用系统代理') -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label','系统代理未启用') -ne [IntPtr]::Zero
    }
    $afterSelection = Get-ItemProperty $internetSettings
    if ($afterSelection.ProxyEnable -ne $beforeSelection.ProxyEnable -or $afterSelection.ProxyServer -ne $beforeSelection.ProxyServer -or (Get-ProxyActionCount) -ne $actionsBeforeSelection) {
        throw 'Changing the selection attempted to update the Windows proxy without confirmation.'
    }
    [KeyDockWindowProbe]::Click([KeyDockWindowProbe]::Child($handle,'button','启用系统代理'))
    $null = Wait-For 'enabled Windows proxy and matching UI state' {
        $button = [KeyDockWindowProbe]::Child($handle,'button','关闭系统代理')
        if ($button -ne [IntPtr]::Zero -and [KeyDockWindowProbe]::Child($handle,'label','系统代理已启用') -ne [IntPtr]::Zero) { $button }
    }
    $updatedAt = [KeyDockWindowProbe]::Text([KeyDockWindowProbe]::Child($handle,'label-prefix','最近更新时间 '))
    if ($updatedAt -notmatch '^最近更新时间 \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$') {
        throw 'The proxy card does not display a full subscription update timestamp.'
    }
    [KeyDockWindowProbe]::AssertReadableLabel($handle,$updatedAt)
    $inet = Get-ItemProperty $internetSettings
    if ($inet.ProxyEnable -ne 1 -or $inet.ProxyServer -notmatch '^127\.0\.0\.1:[0-9]+$') { throw 'UI proxy status does not match Windows Internet Settings.' }
    $actionsBeforeSelection = Get-ProxyActionCount
    [KeyDockWindowProbe]::SelectMode($handle,1)
    $null = Wait-For 'active system proxy awaiting confirmation to switch to TUN' {
        [KeyDockWindowProbe]::Child($handle,'button','切换至 TUN') -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label','系统代理已启用') -ne [IntPtr]::Zero
    }
    $pending = Get-ItemProperty $internetSettings
    if ($pending.ProxyEnable -ne $inet.ProxyEnable -or $pending.ProxyServer -ne $inet.ProxyServer -or (Get-ProxyActionCount) -ne $actionsBeforeSelection) {
        throw 'Selecting TUN interrupted the active Windows system proxy.'
    }
    [KeyDockWindowProbe]::SelectMode($handle,0)
    $turnOff = Wait-For 'system proxy selected again' {
        $button = [KeyDockWindowProbe]::Child($handle,'button','关闭系统代理')
        if ($button -ne [IntPtr]::Zero) { $button }
    }
    # The ephemeral fixture CA has no revocation service. Schannel still
    # validates the certificate chain and host and rejects revoked certificates.
    $throughSS = & "$env:SystemRoot\System32\curl.exe" -fsS --ssl-revoke-best-effort --max-time 8 --noproxy unused.invalid --proxy "http://$($inet.ProxyServer)" "$origin/check-proxy"
    if ($LASTEXITCODE -ne 0 -or $throughSS -ne 'KeyDock fixture reached') { throw 'The enabled system proxy did not relay the local TLS fixture over SS2022.' }
    [KeyDockWindowProbe]::Capture($handle,(Join-Path $shots '05-proxy-enabled.png'),'设备接入')
    [KeyDockWindowProbe]::Click($turnOff)
    $null = Wait-For 'disabled system proxy' {
        [KeyDockWindowProbe]::Child($handle,'button','启用系统代理') -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label','系统代理未启用') -ne [IntPtr]::Zero -and
        [KeyDockWindowProbe]::Child($handle,'label',$updatedAt) -ne [IntPtr]::Zero
    }
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
    $updateFlags = $flags + ('/KEYDOCKUPDATEPID=' + $process.Id)
    $updateWhileRunning = Invoke-SmokeProcess 'online installer while app is open' $installer $updateFlags 35
    if ($updateWhileRunning -ne 7 -or $process.HasExited -or (Get-FileHash $config).Hash -ne $managedHash) {
        throw 'Online installer did not preserve the running app and its Codex configuration.'
    }
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
    $physical = [KeyDockWindowProbe]::RightClickTrayIcon($sink)
    Trace-Smoke "Shell icon accepted a physical right click: $physical"
    if ($physical) { Start-Sleep -Milliseconds 400 }
    if ([KeyDockWindowProbe]::TrayPopup($sink) -eq [IntPtr]::Zero) {
        [KeyDockWindowProbe]::OpenTrayMenu($sink)
    }
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
    $exitCode = [KeyDockWindowProbe]::ExitStatus($exitHandle)
    Trace-Smoke "Installed application exited through the tray with code $exitCode"
    if ($exitCode -ne 0) { throw "Tray exit failed (exit $exitCode)" }
    $setupLog = Join-Path $testRoot 'online-update-setup.log'
    $updateAfterExit = Invoke-SmokeProcess 'online installer after app exit' $installer ($updateFlags + ('/LOG="' + $setupLog + '"')) 45
    if ($updateAfterExit -ne 0 -or -not (Test-Path $exe) -or (Get-FileHash $config).Hash -ne $managedHash) {
        if (Test-Path -LiteralPath $setupLog) { Get-Content -LiteralPath $setupLog -Tail 40 }
        throw 'Online installer did not preserve the device configuration after app exit.'
    }

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
        if ($process.HasExited -and $null -ne $exitHandle) { Trace-Smoke "Native PID $($process.Id) exited with $([KeyDockWindowProbe]::ExitStatus($exitHandle))" }
        else { Trace-Smoke "Native PID $($process.Id), main HWND $($process.MainWindowHandle)" }
    }
    throw
} finally {
    if ($null -ne $process -and -not $process.HasExited) { $process.Kill(); $null = $process.WaitForExit(5000) }
    if (Test-Path (Join-Path $state 'system-proxy.dpapi')) {
        $null = Invoke-SmokeProcess 'restore fixture Windows proxy' $exe @('--prepare-uninstall') 30
    }
    if ($null -ne $peer -and -not $peer.HasExited) { $peer.Kill(); $null = $peer.WaitForExit(5000) }
    if ($null -ne $exitHandle) { $exitHandle.Dispose() }
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
