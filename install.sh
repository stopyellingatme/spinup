#!/bin/sh
# Download, verify, and install a released spinup binary.
set -eu

fail() {
    printf 'cannot install spinup: %s\nTry: check the release version, tools, and destination directory\n' "$*" >&2
    exit 1
}

version=
install_dir=${HOME:?HOME is not set}/.local/bin
while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || fail '--version requires a value'
            version=${2#v}
            shift 2
            ;;
        --dir)
            [ "$#" -ge 2 ] || fail '--dir requires a value'
            install_dir=$2
            shift 2
            ;;
        --help|-h)
            printf '%s\n' 'Usage: install.sh [--version X.Y.Z] [--dir DIR]'
            exit 0
            ;;
        *) fail "unknown option: $1" ;;
    esac
done
[ -n "$install_dir" ] || fail 'the destination directory is empty'
case "$install_dir" in -*) install_dir=./$install_dir ;; esac
command -v curl >/dev/null 2>&1 || fail 'curl is missing'
case $(uname -s) in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    MINGW*|MSYS*|CYGWIN*) os=windows ;;
    *) fail 'the operating system is unsupported' ;;
esac
case $(uname -m) in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail 'the architecture is unsupported' ;;
esac
if command -v sha256sum >/dev/null 2>&1; then
    hash_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
    hash_tool=shasum
else
    fail 'sha256sum or shasum is required'
fi
repository=https://github.com/stopyellingatme/spinup
if [ -z "$version" ]; then
    latest=$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --output /dev/null --write-out '%{url_effective}' "$repository/releases/latest") || fail 'the latest release cannot be resolved'
    version=${latest##*/v}
fi
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || fail 'the release version is invalid'
ext=tar.gz
binary=spinup
if [ "$os" = windows ]; then
    ext=zip
    binary=spinup.exe
    command -v unzip >/dev/null 2>&1 || fail 'unzip is missing'
else
    command -v tar >/dev/null 2>&1 || fail 'tar is missing'
fi
asset=spinup_${version}_${os}_${arch}.${ext}
base=$repository/releases/download/v$version
work=$(mktemp -d) || fail 'a temporary directory cannot be created'
staged=
trap 'rm -rf "$work"; if [ -n "$staged" ]; then rm -f "$staged"; fi' 0
trap 'exit 1' HUP INT TERM
fetch() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        "$base/$1" --output "$work/$1" || fail "download failed: $1"
}
fetch "$asset"
fetch checksums.txt
if command -v cosign >/dev/null 2>&1; then
    fetch checksums.txt.sigstore.json
    cosign verify-blob --bundle "$work/checksums.txt.sigstore.json" \
        --certificate-identity "$repository/.github/workflows/release.yml@refs/tags/v$version" \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com \
        "$work/checksums.txt" || fail 'the checksum signature is invalid'
fi
expected=$(awk -v asset="$asset" '$2 == asset {count++; digest=$1} END {if (count != 1) exit 1; print digest}' "$work/checksums.txt") || fail 'the checksum entry is missing or duplicated'
[ "${#expected}" -eq 64 ] || fail 'the checksum has an invalid length'
case "$expected" in *[!0-9a-fA-F]*) fail 'the checksum is invalid' ;; esac
if [ "$hash_tool" = shasum ]; then
    actual=$(shasum -a 256 "$work/$asset") || fail 'the archive checksum cannot be calculated'
else
    actual=$(sha256sum "$work/$asset") || fail 'the archive checksum cannot be calculated'
fi
actual=${actual%% *}
[ "$actual" = "$expected" ] || fail 'the archive checksum does not match'
if [ "$os" = windows ]; then
    unzip -q "$work/$asset" "$binary" -d "$work" || fail 'the binary cannot be extracted'
else
    tar -xzf "$work/$asset" -C "$work" "$binary" || fail 'the binary cannot be extracted'
fi
[ -f "$work/$binary" ] && [ ! -L "$work/$binary" ] || fail 'the archive has no regular binary'
mkdir -p "$install_dir" || fail 'the destination directory cannot be created'
staged=$(mktemp "$install_dir/.spinup.XXXXXX") || fail 'the destination directory is not writable'
cp "$work/$binary" "$staged" || fail 'the binary cannot be copied'
chmod 755 "$staged" || fail 'the executable mode cannot be set'
mv -f "$staged" "$install_dir/$binary" || fail 'the binary cannot be installed'
staged=
printf 'Installed spinup %s to %s/%s\n' "$version" "$install_dir" "$binary"
