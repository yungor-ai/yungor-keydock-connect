#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_TEMP:?GitHub Actions runner temp directory is required}"
: "${MACOS_DEVELOPER_ID_P12_BASE64:?Developer ID certificate secret is missing}"
: "${MACOS_DEVELOPER_ID_P12_PASSWORD:?Developer ID certificate password secret is missing}"
: "${MACOS_NOTARY_KEY_BASE64:?Apple notarization API key secret is missing}"
: "${MACOS_NOTARY_KEY_ID:?Apple notarization API key ID secret is missing}"
: "${MACOS_NOTARY_ISSUER_ID:?Apple notarization issuer ID secret is missing}"

umask 077
scratch=$(mktemp -d "$RUNNER_TEMP/keydock-signing.XXXXXX")
keychain="$scratch/release.keychain-db"
certificate="$scratch/developer-id.p12"
notary_key="$scratch/notary.p8"
cleanup() {
    security delete-keychain "$keychain" >/dev/null 2>&1 || true
    rm -rf -- "$scratch"
}
trap cleanup EXIT

printf '%s' "$MACOS_DEVELOPER_ID_P12_BASE64" | base64 -D > "$certificate"
printf '%s' "$MACOS_NOTARY_KEY_BASE64" | base64 -D > "$notary_key"
keychain_password=$(openssl rand -hex 24)
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 5400 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$certificate" -f pkcs12 -k "$keychain" -P "$MACOS_DEVELOPER_ID_P12_PASSWORD" -T /usr/bin/codesign
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$keychain_password" "$keychain" >/dev/null

identity=$(security find-identity -v -p codesigning "$keychain" | awk '/"Developer ID Application:/ { count++; identity = $2 } END { if (count == 1) print identity }')
if [[ ! "$identity" =~ ^[0-9A-F]{40}$ ]]; then
    echo 'The release keychain must contain exactly one valid Developer ID Application identity.' >&2
    exit 1
fi

profile=keydock-release
xcrun notarytool store-credentials "$profile" --keychain "$keychain" \
    --key "$notary_key" --key-id "$MACOS_NOTARY_KEY_ID" --issuer "$MACOS_NOTARY_ISSUER_ID" >/dev/null
rm -f -- "$certificate" "$notary_key"
unset MACOS_DEVELOPER_ID_P12_BASE64 MACOS_DEVELOPER_ID_P12_PASSWORD
unset MACOS_NOTARY_KEY_BASE64 MACOS_NOTARY_KEY_ID MACOS_NOTARY_ISSUER_ID keychain_password

KEYDOCK_CODESIGN_IDENTITY="$identity" KEYDOCK_NOTARY_PROFILE="$profile" KEYDOCK_NOTARY_KEYCHAIN="$keychain" \
    bash packaging/build-macos.sh
