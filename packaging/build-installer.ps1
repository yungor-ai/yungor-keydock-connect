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

# Inno 7 ships the simplified Chinese catalog. Inno 6 only carries it in
# its source repository; verify that exact translation before compiling it.
$langDir = New-Item -Force -ItemType Directory 'dist/inno-language'
$chinese = Join-Path $langDir.FullName 'ChineseSimplified.isl'
$included = Join-Path (Split-Path $compiler) 'Languages\ChineseSimplified.isl'
if (Test-Path $included) {
    Copy-Item $included $chinese -Force
} else {
    $url = 'https://raw.githubusercontent.com/jrsoftware/issrc/c2913c993858a781fcb455bac8b8949c1c366cb8/Files/Languages/Unofficial/ChineseSimplified.isl'
    Invoke-WebRequest -Uri $url -OutFile $chinese
    if ((Get-FileHash $chinese).Hash -ne '7d544b9bb1d142cfa11f2e5d3cc8abe2e55f8e066c5124e3772675aa236e1278') {
        throw 'Inno Setup language file checksum mismatch.'
    }
}

$version = (Get-Content -Raw -LiteralPath 'VERSION').Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw "Invalid installer version in VERSION." }
Copy-Item -LiteralPath 'README.md' -Destination 'dist/app/README.md' -Force
& $compiler "/DAppVersion=$version" 'packaging/keydock-connect.iss'
if ($LASTEXITCODE -ne 0) { throw "Inno Setup failed with exit code $LASTEXITCODE." }
$installer = Get-Item 'dist/installer/KeyDockConnect-Setup-windows-x64.exe'
$portablePath = Join-Path $installer.DirectoryName 'KeyDockConnect-windows-x64.zip'
Compress-Archive -Path 'dist/app/*' -DestinationPath $portablePath -CompressionLevel Optimal -Force
$portable = Get-Item -LiteralPath $portablePath
[string[]]$checksums = foreach ($asset in @($installer, $portable)) {
    $digest = (Get-FileHash -LiteralPath $asset.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$digest  $($asset.Name)"
}
[IO.File]::WriteAllLines((Join-Path $installer.DirectoryName 'SHA256SUMS.txt'), $checksums)
