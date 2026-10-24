#!/usr/bin/env bash
# Install the CI tools that tests need: actionlint, shellcheck, and bun.
# Usage: scripts/ci/install-tools.sh <actionlint|shellcheck|bun>...
#
# Every download is checksum verified. The versions of actionlint and bun come from
# internal/stackversions/stackversions.yaml. The shellcheck pin lives in this file.
# The tools go to $SPINUP_CI_PREFIX/bin (default $HOME/.spinup-ci/bin).
# In GitHub Actions the script adds that directory to PATH for the next steps.
# A tool that is already on PATH at the right version is not installed again.
set -euo pipefail

here=$(cd "$(dirname "$0")/../.." && pwd)
stack="$here/internal/stackversions/stackversions.yaml"
prefix=${SPINUP_CI_PREFIX:-$HOME/.spinup-ci}
bin="$prefix/bin"

SHELLCHECK_VERSION=0.11.0
SHELLCHECK_SHA256_DARWIN_ARM64=339b930feb1ea764467013cc1f72d09cd6b869ebf1013296ba9055ab2ffbd26f
SHELLCHECK_SHA256_DARWIN_AMD64=c2c15e08df0e8fbc374c335b230a7ee958c313fa5714817a59aa59f1aa594f51

die() {
  printf 'install-tools: %s\nTry: %s\n' "$1" "$2" >&2
  exit 1
}

pin() { # key: print the first "  key: value" line of stackversions.yaml
  local value
  value=$(awk -v key="$1:" '$1 == key { print $2; exit }' "$stack")
  [[ -n $value ]] || die "no pin for $1 in $stack" "add the pin to stackversions.yaml"
  printf '%s\n' "$value"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

verify() { # file, expected sha256
  local actual
  actual=$(sha256_of "$1")
  [[ $actual == "$2" ]] || die "checksum mismatch for $(basename "$1"): got $actual, want $2" "retry the job or report a changed release"
}

fetch() { # url, output file
  curl --fail --silent --show-error --location --retry 3 "$1" --output "$2"
}

platform() {
  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported operating system $(uname -s)" "run this script on Linux or macOS" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) die "unsupported CPU $(uname -m)" "run this script on amd64 or arm64" ;;
  esac
  printf '%s %s\n' "$os" "$arch"
}

have_version() { # command, version
  command -v "$1" >/dev/null 2>&1 && "$1" --version 2>&1 | grep -q -F "$2"
}

install_actionlint() {
  local version os arch name url temp expected
  version=$(pin actionlint)
  if have_version actionlint "$version"; then return; fi
  read -r os arch <<<"$(platform)"
  name="actionlint_${version}_${os}_${arch}.tar.gz"
  url="https://github.com/rhysd/actionlint/releases/download/v$version"
  temp=$(mktemp -d)
  fetch "$url/$name" "$temp/$name"
  fetch "$url/actionlint_${version}_checksums.txt" "$temp/checksums.txt"
  expected=$(awk -v name="$name" '$2 == name { print $1 }' "$temp/checksums.txt")
  [[ -n $expected ]] || die "no checksum for $name" "check the actionlint release"
  verify "$temp/$name" "$expected"
  tar -xzf "$temp/$name" -C "$temp" actionlint
  mkdir -p "$bin"
  install -m 0755 "$temp/actionlint" "$bin/actionlint"
  rm -rf "$temp"
}

install_shellcheck() {
  local os arch expected temp name
  if command -v shellcheck >/dev/null 2>&1; then return; fi
  read -r os arch <<<"$(platform)"
  [[ $os == darwin ]] || die "shellcheck is missing on $os" "install the shellcheck package"
  if [[ $arch == arm64 ]]; then
    expected=$SHELLCHECK_SHA256_DARWIN_ARM64
    name=darwin.aarch64
  else
    expected=$SHELLCHECK_SHA256_DARWIN_AMD64
    name=darwin.x86_64
  fi
  temp=$(mktemp -d)
  fetch "https://github.com/koalaman/shellcheck/releases/download/v$SHELLCHECK_VERSION/shellcheck-v$SHELLCHECK_VERSION.$name.tar.gz" "$temp/shellcheck.tar.gz"
  verify "$temp/shellcheck.tar.gz" "$expected"
  tar -xzf "$temp/shellcheck.tar.gz" -C "$temp"
  mkdir -p "$bin"
  install -m 0755 "$temp/shellcheck-v$SHELLCHECK_VERSION/shellcheck" "$bin/shellcheck"
  rm -rf "$temp"
}

install_bun() {
  local version
  version=$(pin bun)
  if have_version bun "$version"; then return; fi
  command -v npm >/dev/null 2>&1 || die "npm is not on PATH" "set up Node before this step"
  # The npm registry checks the integrity hash of bun and of its platform package.
  npm install --global --prefix "$prefix" --no-audit --no-fund "bun@$version"
}

[[ $# -gt 0 ]] || die "no tool named" "pass actionlint, shellcheck, or bun"
for tool in "$@"; do
  case "$tool" in
    actionlint) install_actionlint ;;
    shellcheck) install_shellcheck ;;
    bun) install_bun ;;
    *) die "unknown tool $tool" "pass actionlint, shellcheck, or bun" ;;
  esac
done

if [[ -n ${GITHUB_PATH:-} ]]; then
  printf '%s\n' "$bin" >>"$GITHUB_PATH"
fi
export PATH="$bin:$PATH"
for tool in "$@"; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not on PATH after the install" "check $bin"
  printf '%s: %s\n' "$tool" "$(command -v "$tool")"
done
