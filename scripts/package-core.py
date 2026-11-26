#!/usr/bin/env python3
"""Add the pinned proxy core, offline routing data and sources to a bundle."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile

CORE_TAG = "v1.19.34"
CORE_COMMIT = "0455d0d28c4526937380505153e65a843d8612e8"
GEO_DATA = "7ff38ff75b48262b891c1122217555fb039631a6"
GEO_SOURCE = "4178770badecb1b349fbcd62c737e0d7a2079729"
GEO_BASE = f"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/{GEO_DATA}/"
COMMON_ASSETS = {
    "mihomo-source.tar.gz": (f"https://codeload.github.com/MetaCubeX/mihomo/tar.gz/{CORE_COMMIT}",
                             "db0cc4145fe4024e9c2386bd4768bf23a69c8789610d56d7243819e2f77c0cfe"),
    "GeoSite.dat": (GEO_BASE + "geosite.dat", "2bdf171e1862b47cc5cd322ee43e5d066d9963025919ebb58dc7891d2b92643e"),
    "geoip.metadb": (GEO_BASE + "geoip.metadb", "4236cf8477d12ceb4837eff39a03a65d5d55a72d3cccdc92e2e36d9cdbd686e3"),
    "meta-rules-source.tar.gz": (f"https://codeload.github.com/MetaCubeX/meta-rules-dat/tar.gz/{GEO_SOURCE}",
                                 "6e8d87b0439cfe46618a55f0d8c90fea07450c59f586d1dab48d920f2e5db0a0"),
}
WINDOWS_ASSET = (f"https://github.com/MetaCubeX/mihomo/releases/download/{CORE_TAG}/mihomo-windows-amd64-compatible-{CORE_TAG}.zip",
                 "cd6738d4648cc1a5f48049a1343b697ab510889d648f045c5c780fc1ab443a79")
MACOS_ASSET = (f"https://github.com/MetaCubeX/mihomo/releases/download/{CORE_TAG}/mihomo-darwin-arm64-{CORE_TAG}.gz",
               "1a7a20eca63fbbaec90137717465bfde488d0db2a3a79b8fadebf9b2545b6e4c")

def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as src:
        for block in iter(lambda: src.read(1 << 20), b""):
            checksum.update(block)
    return checksum.hexdigest()

def fetch(cache, name, url, expected):
    cache.mkdir(parents=True, exist_ok=True)
    target = cache / (expected + "-" + name)
    if target.exists() and digest(target) == expected:
        return target
    with tempfile.NamedTemporaryFile(dir=cache, delete=False) as handle:
        tmp = Path(handle.name)
    try:
        with urllib.request.urlopen(url, timeout=90) as src, tmp.open("wb") as dst:
            shutil.copyfileobj(src, dst, length=1 << 20)
        if digest(tmp) != expected:
            raise ValueError(f"SHA-256 mismatch for {name}")
        os.replace(tmp, target)
    finally:
        tmp.unlink(missing_ok=True)
    return target

def unpack_source(path, dest, prefix):
    with tarfile.open(path) as archive:
        for name in ("LICENSE", "README.md"):
            member = archive.getmember(prefix + "/" + name)
            if not member.isfile() or member.size > 1 << 20:
                raise ValueError("Unexpected upstream notice")
            (dest / name).write_bytes(archive.extractfile(member).read())
    shutil.copyfile(path, dest / "source.tar.gz")

NOTICE_NAME = re.compile(r"^(licen[sc]e|copying|notice|patents)(?:$|[-._].*)", re.IGNORECASE)

def go_notices(destination, platform):
    """Collect notices from the exact toolchain and the executable's build graph."""
    if platform == "macos-arm64":
        environment = dict(os.environ, GOOS="darwin", GOARCH="arm64", CGO_ENABLED="1")
        product = "macOS arm64"
    else:
        environment = dict(os.environ, GOOS="windows", GOARCH="amd64", CGO_ENABLED="0")
        product = "Windows x64"
    root = Path(__file__).resolve().parent.parent

    def go(*args):
        return subprocess.check_output(["go", *args], cwd=root, env=environment, encoding="utf-8")

    info = json.loads(go("env", "-json", "GOROOT", "GOVERSION"))
    goroot = Path(info["GOROOT"])
    version = info["GOVERSION"]
    stdlib = destination / "go-runtime"
    stdlib.mkdir(parents=True)
    # Homebrew relocates the distribution's LICENSE to the parent of libexec.
    runtime_license = goroot / "LICENSE"
    if not runtime_license.is_file():
        runtime_license = goroot.parent / "LICENSE"
    if not runtime_license.is_file() or "The Go Authors" not in runtime_license.read_text(encoding="utf-8"):
        raise RuntimeError(f"Cannot locate the Go distribution license for {version}")
    shutil.copyfile(runtime_license, stdlib / "LICENSE")

    def copy_notices(folder, target):
        notices = [f for f in folder.iterdir() if f.is_file() and NOTICE_NAME.match(f.name)]
        for notice in sorted(notices):
            target.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(notice, target / notice.name)
        return notices

    copy_notices(goroot, stdlib)
    # A package can inherit a notice from the module root or any parent, e.g.
    # GOROOT/src/vendor/golang.org/x/text/LICENSE.
    graph = go("list", "-deps", "-json=Dir,Standard,Module", "./cmd/keydock-connect")
    decoder = json.JSONDecoder()
    visited = set()
    modules = set()
    position = 0
    while position < len(graph):
        if graph[position].isspace():
            position += 1
            continue
        package, position = decoder.raw_decode(graph, position)
        directory = Path(package["Dir"])
        if package.get("Standard"):
            project, target = goroot, stdlib
        else:
            module = package["Module"]
            if module.get("Main"):
                continue
            resolved = module.get("Replace", module)
            project = Path(resolved["Dir"])
            identifier = module["Path"] + "@" + (resolved.get("Version") or "local")
            target = destination / "go-modules" / identifier
            modules.add(identifier)

        while directory != project:
            relative = directory.relative_to(project)
            if directory in visited:
                break
            visited.add(directory)
            copy_notices(directory, target / relative)
            directory = directory.parent

        if project != goroot and project not in visited:
            visited.add(project)
            if not copy_notices(project, target):
                raise RuntimeError(f"Missing upstream license for {identifier}")

    (destination / "GO-COMPONENTS.txt").write_text(
        f"KeyDock Connect {product}\nGo runtime and standard library: " + version
        + "\n\nLinked modules:\n" + "\n".join(sorted(modules)) + "\n", encoding="utf-8")


def unpack_core(assets, directory, macos):
    if macos:
        executable = directory / "mihomo"
        with gzip.open(assets["core-macos-arm64.gz"], "rb") as src, executable.open("wb") as dst:
            shutil.copyfileobj(src, dst)
        expected = "a55a9073f9b7b1fa596a82c034d2dae3407ec34732102b6e42c5513ce3033f34"
    else:
        executable = directory / "mihomo.exe"
        with zipfile.ZipFile(assets["core.zip"]) as archive:
            with archive.open("mihomo-windows-amd64-compatible.exe") as src, executable.open("wb") as dst:
                shutil.copyfileobj(src, dst)
        expected = "5a0231811a80b27477ebdee15684b41946d01afbd2114534e9a4b256125ca147"
    if digest(executable) != expected:
        raise ValueError("Unexpected core executable")
    if macos:
        executable.chmod(0o755)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path, nargs="?", default=Path("dist/app"))
    parser.add_argument("--platform", choices=("windows-x64", "macos-arm64"), default="windows-x64")
    parser.add_argument("--cache-dir", type=Path, default=Path.home() / ".cache" / "keydock-connect" / "downloads")
    args = parser.parse_args()
    macos = args.platform == "macos-arm64"
    platform_assets = {"core-macos-arm64.gz": MACOS_ASSET} if macos else {"core.zip": WINDOWS_ASSET}
    asset_sources = {**platform_assets, **COMMON_ASSETS}
    args.bundle.mkdir(parents=True, exist_ok=True)
    assets = {name: fetch(args.cache_dir, name, *source) for name, source in asset_sources.items()}
    with tempfile.TemporaryDirectory(dir=args.bundle) as directory:
        stage = Path(directory)
        core = stage / "core"
        core.mkdir()
        unpack_core(assets, core, macos)
        for name in ("GeoSite.dat", "geoip.metadb"):
            shutil.copyfile(assets[name], core / name)
        licenses = stage / "licenses"
        go_notices(licenses, args.platform)
        for project, archive, prefix in (
            ("mihomo", "mihomo-source.tar.gz", "mihomo-" + CORE_COMMIT),
            ("meta-rules-dat", "meta-rules-source.tar.gz", "meta-rules-dat-" + GEO_SOURCE),
        ):
            dest = licenses / project
            dest.mkdir(parents=True)
            unpack_source(assets[archive], dest, prefix)
        (licenses / "SOURCES.txt").write_text(
            f"Mihomo {CORE_TAG}, GPL-3.0. Bundled corresponding source: mihomo/source.tar.gz.\n"
            f"Offline GEO data from MetaCubeX/meta-rules-dat release commit {GEO_DATA}.\n"
            "See meta-rules-dat/README.md for upstream datasets, credits and sources.\n"
            "The bundled build instructions, license and scripts are in meta-rules-dat/source.tar.gz.\n\n"
            + "".join(f"{checksum}  {name}\n{url}\n" for name, (url, checksum) in asset_sources.items()),
            encoding="utf-8")
        for file in sorted(stage.rglob("*")):
            if file.is_file():
                dest = args.bundle / file.relative_to(stage)
                dest.parent.mkdir(parents=True, exist_ok=True)
                os.replace(file, dest)
if __name__ == "__main__":
    main()
