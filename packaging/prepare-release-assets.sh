#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 3 ]]; then
    echo 'Usage: prepare-release-assets.sh WINDOWS_DIRECTORY MACOS_DIRECTORY OUTPUT_DIRECTORY' >&2
    exit 2
fi

windows_dir=$1
macos_dir=$2
output=$3
installer=KeyDockConnect-Setup-windows-x64.exe
portable=KeyDockConnect-windows-x64.zip
dmg=KeyDockConnect-macos-arm64.dmg

verify() (
    local folder=$1
    shift
    cd -- "$folder"
    for asset in "$@"; do
        if [[ ! -s "$asset" ]]; then
            echo "Missing or empty release asset: $asset" >&2
            exit 1
        fi
    done
    local actual recorded
    actual=$(shasum -a 256 -- "$@")
    recorded=$(tr -d '\r' < SHA256SUMS.txt)
    if [[ "$actual" != "$recorded" ]]; then
        echo "Release asset checksum mismatch in $folder." >&2
        exit 1
    fi
)

verify "$windows_dir" "$installer" "$portable"
verify "$macos_dir" "$dmg"

mkdir -p -- "$output"
cp -- "$windows_dir/$installer" "$windows_dir/$portable" "$macos_dir/$dmg" "$output/"
(cd -- "$output" && shasum -a 256 -- "$installer" "$portable" "$dmg" > SHA256SUMS.txt)
