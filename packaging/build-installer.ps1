# Run from the checkout root after building dist/app.
$ErrorActionPreference = 'Stop'
$compiler = Get-Command ISCC.exe -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty Source
if (-not $compiler) {
    $compiler = @(
        "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
        "$env:ProgramFiles\Inno Setup 7\ISCC.exe",
        "${env:ProgramFiles(x86)}\Inno Setup 7\ISCC.exe"
    ) | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
}
if (-not $compiler) {
    if ($env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
        throw 'Install Inno Setup before building the installer locally.'
    }
    $bootstrap = Join-Path $env:RUNNER_TEMP 'innosetup-6.7.4.exe'
    $toolDir = Join-Path $env:RUNNER_TEMP 'InnoSetup'
    Invoke-WebRequest -Uri 'https://github.com/jrsoftware/issrc/releases/download/is-6_7_4/innosetup-6.7.4.exe' -OutFile $bootstrap
    $hash = (Get-FileHash $bootstrap -Algorithm SHA256).Hash
    if ($hash -ne 'a549929319f9120716677c685535ca4671e4b937b5fdc87e2585f32d8eb30e8c') {
        throw 'Inno Setup download checksum mismatch.'
    }
    $tool = Start-Process $bootstrap -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/DIR=`"$toolDir`"" -Wait -PassThru
    if ($tool.ExitCode -ne 0) { throw 'Could not install Inno Setup on the runner.' }
    $compiler = Join-Path $toolDir 'ISCC.exe'
}

& $compiler 'packaging/keydock-connect.iss'
if ($LASTEXITCODE -ne 0) { throw "Inno Setup failed with exit code $LASTEXITCODE." }
$installer = Get-Item 'dist/installer/KeyDockConnect-Setup-windows-x64.exe'
$digest = (Get-FileHash $installer.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText((Join-Path $installer.DirectoryName 'SHA256SUMS.txt'), "$digest  $($installer.Name)`n")
