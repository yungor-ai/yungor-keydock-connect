#!/usr/bin/env python3
"""Add the pinned Windows x64 proxy core, offline routing data and sources to a bundle."""
import argparse
import hashlib
import os
from pathlib import Path
import shutil
import tarfile
import tempfile
import urllib.request
import zipfile

CORE_TAG = "v1.19.34"
CORE_COMMIT = "0455d0d28c4526937380505153e65a843d8612e8"
GEO_DATA = "7ff38ff75b48262b891c1122217555fb039631a6"
GEO_SOURCE = "4178770badecb1b349fbcd62c737e0d7a2079729"
GEO_BASE = f"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/{GEO_DATA}/"
ASSETS = {
    "core.zip": (f"https://github.com/MetaCubeX/mihomo/releases/download/{CORE_TAG}/mihomo-windows-amd64-compatible-{CORE_TAG}.zip",
                 "cd6738d4648cc1a5f48049a1343b697ab510889d648f045c5c780fc1ab443a79"),
    "mihomo-source.tar.gz": (f"https://codeload.github.com/MetaCubeX/mihomo/tar.gz/{CORE_COMMIT}",
                             "db0cc4145fe4024e9c2386bd4768bf23a69c8789610d56d7243819e2f77c0cfe"),
    "GeoSite.dat": (GEO_BASE + "geosite.dat", "2bdf171e1862b47cc5cd322ee43e5d066d9963025919ebb58dc7891d2b92643e"),
    "geoip.metadb": (GEO_BASE + "geoip.metadb", "4236cf8477d12ceb4837eff39a03a65d5d55a72d3cccdc92e2e36d9cdbd686e3"),
    "meta-rules-source.tar.gz": (f"https://codeload.github.com/MetaCubeX/meta-rules-dat/tar.gz/{GEO_SOURCE}",
                                 "6e8d87b0439cfe46618a55f0d8c90fea07450c59f586d1dab48d920f2e5db0a0"),
}

def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as src:
        for block in iter(lambda: src.read(1 << 20), b""):
            checksum.update(block)
    return checksum.hexdigest()

def fetch(cache, name):
    url, expected = ASSETS[name]
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

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path, nargs="?", default=Path("dist/app"))
    parser.add_argument("--cache-dir", type=Path, default=Path.home() / ".cache" / "keydock-connect" / "downloads")
    args = parser.parse_args()
    args.bundle.mkdir(parents=True, exist_ok=True)
    assets = {name: fetch(args.cache_dir, name) for name in ASSETS}
    with tempfile.TemporaryDirectory(dir=args.bundle) as directory:
        stage = Path(directory)
        core = stage / "core"
        core.mkdir()
        with zipfile.ZipFile(assets["core.zip"]) as archive:
            with archive.open("mihomo-windows-amd64-compatible.exe") as src, (core / "mihomo.exe").open("wb") as dst:
                shutil.copyfileobj(src, dst)
        if digest(core / "mihomo.exe") != "5a0231811a80b27477ebdee15684b41946d01afbd2114534e9a4b256125ca147":
            raise ValueError("Unexpected core executable")
        for name in ("GeoSite.dat", "geoip.metadb"):
            shutil.copyfile(assets[name], core / name)
        licenses = stage / "licenses"
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
            + "".join(f"{checksum}  {name}\n{url}\n" for name, (url, checksum) in ASSETS.items()),
            encoding="utf-8")
        for file in sorted(stage.rglob("*")):
            if file.is_file():
                dest = args.bundle / file.relative_to(stage)
                dest.parent.mkdir(parents=True, exist_ok=True)
                os.replace(file, dest)
if __name__ == "__main__":
    main()
