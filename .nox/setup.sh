#!/bin/sh
set -eu

go_version=1.26.0
required_go="$(awk '$1 == "go" { print $2; exit }' go.mod)"
case "$required_go" in
  1.26|1.26.0) ;;
  *)
    printf 'go.mod requires Go %s, but .nox/setup.sh pins Go %s\n' "$required_go" "$go_version" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64)
    go_arch=amd64
    go_sha256=aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235
    ;;
  aarch64|arm64)
    go_arch=arm64
    go_sha256=bd03b743eb6eb4193ea3c3fd3956546bf0e3ca5b7076c8226334afe6b75704cd
    ;;
  *)
    printf 'unsupported architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends gcc libc6-dev
rm -rf /var/lib/apt/lists/*

install_dir="/opt/nox/toolchains/go/$go_version"
if [ ! -x "$install_dir/bin/go" ]; then
  archive="go$go_version.linux-$go_arch.tar.gz"
  temporary_dir="$(mktemp -d /var/tmp/nox-go.XXXXXX)"
  trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM
  curl -fsSL "https://go.dev/dl/$archive" -o "$temporary_dir/$archive"
  printf '%s  %s\n' "$go_sha256" "$temporary_dir/$archive" | sha256sum -c -
  tar -C "$temporary_dir" -xzf "$temporary_dir/$archive"
  mkdir -p "$(dirname "$install_dir")"
  mv "$temporary_dir/go" "$install_dir"
  rm -rf "$temporary_dir"
  trap - EXIT HUP INT TERM
fi

ln -sf "$install_dir/bin/go" /usr/local/bin/go
ln -sf "$install_dir/bin/gofmt" /usr/local/bin/gofmt
GOTOOLCHAIN=local go version
GOTOOLCHAIN=local go mod download
