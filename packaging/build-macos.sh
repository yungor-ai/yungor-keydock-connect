#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
    echo "Build KeyDock Connect on an Apple Silicon Mac." >&2
    exit 2
fi

cd "$(dirname "$0")/.."
app_name='KeyDock Connect.app'
image_name='KeyDockConnect-macos-arm64.dmg'
output="$PWD/dist/macos"
identity=${KEYDOCK_CODESIGN_IDENTITY:-}
notary_profile=${KEYDOCK_NOTARY_PROFILE:-}
keychain=${KEYDOCK_NOTARY_KEYCHAIN:-}
if [[ -n "$identity" || -n "$notary_profile" || -n "$keychain" ]]; then
    if [[ -z "$identity" || "$identity" == - || -z "$notary_profile" ]]; then
        echo 'Set KEYDOCK_CODESIGN_IDENTITY to a Developer ID Application identity and KEYDOCK_NOTARY_PROFILE to a notarytool profile.' >&2
        exit 2
    fi
fi
version=$(<VERSION)
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Invalid macOS bundle version in VERSION." >&2
    exit 2
fi

mkdir -p "$output"
stage=$(mktemp -d "$output/.build.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT
folder="$stage/image"
app="$folder/$app_name"
contents="$app/Contents"
binary="$contents/MacOS/KeyDockConnect"
resources="$contents/Resources"
core="$contents/Helpers/mihomo"
plist="$contents/Info.plist"
daemon_plist="$contents/Library/LaunchDaemons/ai.yungor.keydock-connect.tun.plist"
mkdir -p "$contents/MacOS" "$contents/Helpers" "$(dirname "$daemon_plist")" "$resources"

export MACOSX_DEPLOYMENT_TARGET=13.0
# Put the target in cgo's cache key as well as the final link environment.
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
# package-core.py checks the SHA-256 of the official archive and its executable.
python3 scripts/package-core.py --platform macos-arm64 "$resources"
mv -- "$resources/core/mihomo" "$core"
ln -s ../../Helpers/mihomo "$resources/core/mihomo"
codesign --verify --strict "$core"

linker_flags='-s -w'
app_signing_options=(--sign -)
if [[ -n "$identity" ]]; then
    signing_options=(--sign "$identity" --timestamp)
    if [[ -n "$keychain" ]]; then
        signing_options+=(--keychain "$keychain")
    fi
    app_signing_options=("${signing_options[@]}" --options runtime)
    codesign --force "${app_signing_options[@]}" --identifier ai.yungor.keydock-connect.mihomo "$core"
    codesign --verify --strict "$core"
    signed_core_hash=$(shasum -a 256 "$core")
    linker_flags+=" -X github.com/yungor-ai/yungor-keydock-connect/internal/proxy.coreDigest=${signed_core_hash%% *}"
fi
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -trimpath -ldflags="$linker_flags" -o "$binary" ./cmd/keydock-connect

cp packaging/macos-info.plist "$plist"
plutil -insert CFBundleShortVersionString -string "$version" "$plist"
plutil -insert CFBundleVersion -string "$version" "$plist"
plutil -lint "$plist"
cp packaging/macos-tun-launchd.plist "$daemon_plist"
plutil -lint "$daemon_plist"

iconset="$stage/KeyDock.iconset"
mkdir "$iconset"
sips -s format png cmd/keydock-connect/keydock.ico --out "$stage/icon.png" >/dev/null
for size in 16 32 128 256 512; do
    double=$((2 * size))
    sips -z "$size" "$size" "$stage/icon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
    sips -z "$double" "$double" "$stage/icon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil --convert icns --output "$resources/KeyDock.icns" "$iconset"

for executable in "$binary" "$core"; do
    if [[ "$(lipo -archs "$executable")" != arm64 ]]; then
        echo "Expected a native arm64 executable at $executable." >&2
        exit 1
    fi
done
codesign --force "${app_signing_options[@]}" "$app"
codesign --verify --deep --strict "$app"

ln -s /Applications "$folder/Applications"
image="$stage/$image_name"
hdiutil create -quiet -volname 'KeyDock Connect' -srcfolder "$folder" -format UDZO -fs HFS+ "$image"
hdiutil verify -quiet "$image"
if [[ -n "$identity" ]]; then
    codesign "${signing_options[@]}" --identifier ai.yungor.keydock-connect.disk-image "$image"
    codesign --verify --strict "$image"
    notary_options=(--keychain-profile "$notary_profile")
    if [[ -n "$keychain" ]]; then
        notary_options+=(--keychain "$keychain")
    fi
    if ! result=$(xcrun notarytool submit "$image" "${notary_options[@]}" --wait --timeout 30m --output-format plist); then
        printf '%s\n' "$result" >&2
        exit 1
    fi
    printf '%s\n' "$result"
    if [[ "$(plutil -extract status raw -o - -- - <<<"$result")" != Accepted ]]; then
        echo 'Apple did not accept the disk image for notarization.' >&2
        exit 1
    fi
    xcrun stapler staple "$image"
    xcrun stapler validate "$image"
    codesign --verify --strict "$image"
fi
(cd "$stage" && shasum -a 256 "$image_name" > SHA256SUMS.txt)

if [[ -e "$output/$app_name" ]]; then
    mv -- "$output/$app_name" "$stage/previous.app"
fi
mv -- "$app" "$output/$app_name"
mv -f -- "$stage/$image_name" "$stage/SHA256SUMS.txt" "$output/"
echo "$output/$image_name"
