#!/usr/bin/env bash
set -euo pipefail

# Install Nox from a checkout or source archive. The default installs only
# the CLI; --local also prepares the local Docker/gVisor backend.

SOURCE_DIR=""
SOURCE_TEMP=""
BUILD_BINARY=""
TEMP_BINARY=""
SOURCE_REPO="${NOX_SOURCE_REPO:-https://github.com/nox-dev/nox}"
SOURCE_REF="${NOX_SOURCE_REF:-main}"
SOURCE_ARCHIVE_URL="${NOX_SOURCE_ARCHIVE_URL:-}"
if [ -z "$SOURCE_ARCHIVE_URL" ]; then
	if [[ "$SOURCE_REF" =~ ^[0-9a-fA-F]{40}$ ]]; then
		SOURCE_ARCHIVE_URL="${SOURCE_REPO%/}/archive/${SOURCE_REF}.tar.gz"
	else
		SOURCE_ARCHIVE_URL="${SOURCE_REPO%/}/archive/refs/heads/${SOURCE_REF}.tar.gz"
	fi
fi
PREFIX="${NOX_PREFIX:-${HOME}/.local/bin}"
IMAGE="${NOX_RUNNER_IMAGE:-nox-runner:v0}"
PROFILE="${NOX_COLIMA_PROFILE:-nox}"
GVISOR_VERSION="${NOX_GVISOR_VERSION:-latest}"
GVISOR_FORCE="${NOX_GVISOR_FORCE_INSTALL:-0}"
if [ -n "${NOX_GVISOR_VERSION:-}" ]; then
	GVISOR_FORCE=1
fi
COLIMA_CPUS="${NOX_COLIMA_CPUS:-4}"
COLIMA_MEMORY="${NOX_COLIMA_MEMORY:-8}"
COLIMA_DISK="${NOX_COLIMA_DISK:-40}"
COLIMA_VM_TYPE="${NOX_COLIMA_VM_TYPE:-vz}"
LOCAL=0

usage() {
	cat <<'EOF'
Usage: install.sh [options]

Install the Nox CLI from this checkout or a downloaded source archive.
By default, only the CLI is built.

Options:
  --local                  also prepare the local Docker/gVisor backend
  --prefix PATH            install the binary under PATH
  --image IMAGE            runner image tag to build and validate
  --profile NAME           Colima profile name (macOS --local only)
  --gvisor-version VALUE   gVisor release path, default: latest
  -h, --help               show this help

Environment overrides:
  NOX_PREFIX, NOX_RUNNER_IMAGE, NOX_COLIMA_PROFILE
  NOX_SOURCE_REPO, NOX_SOURCE_REF, NOX_SOURCE_ARCHIVE_URL
  NOX_GVISOR_VERSION, NOX_GVISOR_FORCE_INSTALL
  NOX_COLIMA_CPUS, NOX_COLIMA_MEMORY, NOX_COLIMA_DISK
  NOX_COLIMA_VM_TYPE

Examples:
  curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- --local
  ./install.sh --local --prefix "$HOME/.local/bin"
EOF
}

die() {
	printf 'install: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '==> %s\n' "$*"
}

has_command() {
	command -v "$1" >/dev/null 2>&1
}

require_command() {
	has_command "$1" || die "missing required command: $1"
}

cleanup_source() {
	if [ -n "$SOURCE_TEMP" ]; then
		rm -rf "$SOURCE_TEMP"
	fi
}

cleanup() {
	if [ -n "$BUILD_BINARY" ]; then
		rm -f "$BUILD_BINARY"
	fi
	if [ -n "$TEMP_BINARY" ]; then
		rm -f "$TEMP_BINARY"
	fi
	cleanup_source
}

trap cleanup EXIT

prepare_source() {
	local script_path="${BASH_SOURCE[0]:-}"
	local candidate=""
	if [ -n "$script_path" ] && [ -f "$script_path" ]; then
		candidate="$(cd "$(dirname "$script_path")" && pwd)"
	fi
	if [ -n "$candidate" ] && [ -f "$candidate/go.mod" ] && [ -d "$candidate/images/runner" ]; then
		SOURCE_DIR="$candidate"
		return
	fi

	require_command curl
	require_command tar
	SOURCE_TEMP="$(mktemp -d "${TMPDIR:-/tmp}/nox-source.XXXXXX")"
	log "downloading Nox source from $SOURCE_ARCHIVE_URL"
	curl -fsSL "$SOURCE_ARCHIVE_URL" -o "$SOURCE_TEMP/source.tar.gz"
	if tar -tzf "$SOURCE_TEMP/source.tar.gz" | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
		die "source archive contains an unsafe path"
	fi
	tar -xzf "$SOURCE_TEMP/source.tar.gz" -C "$SOURCE_TEMP"
	local candidate_dir
	for candidate_dir in "$SOURCE_TEMP"/*; do
		[ -d "$candidate_dir" ] || continue
		[ -z "$SOURCE_DIR" ] || die "source archive contains multiple top-level directories"
		SOURCE_DIR="$candidate_dir"
	done
	[ -n "$SOURCE_DIR" ] || die "source archive did not contain a top-level directory"
	[ -f "$SOURCE_DIR/go.mod" ] && [ -d "$SOURCE_DIR/images/runner" ] || \
		die "source archive is not a Nox source tree"
}

install_macos_dependency() {
	local formula="$1"
	local command_name="$2"
	if has_command "$command_name"; then
		return
	fi
	if ! has_command brew; then
		die "missing $command_name; install Homebrew or install $formula manually"
	fi
	log "installing Homebrew package $formula"
	brew install "$formula"
}

start_colima() {
	require_command colima
	require_command docker

	if ! colima status "$PROFILE" >/dev/null 2>&1; then
		log "starting Colima profile $PROFILE"
		colima start "$PROFILE" \
			--runtime docker \
			--vm-type "$COLIMA_VM_TYPE" \
			--cpus "$COLIMA_CPUS" \
			--memory "$COLIMA_MEMORY" \
			--disk "$COLIMA_DISK"
	else
		log "reusing running Colima profile $PROFILE"
	fi

	local context="colima-${PROFILE}"
	docker context inspect "$context" >/dev/null 2>&1 || \
		die "Colima did not create Docker context $context"
	local status_json
	status_json="$(colima status "$PROFILE" --json)"
	printf '%s' "$status_json" | grep -q '"runtime":"docker"' || \
		die "Colima profile $PROFILE is not using the Docker runtime"
	local colima_socket
	local context_socket
	colima_socket="$(printf '%s' "$status_json" | sed -n 's/.*"docker_socket":"\([^"]*\)".*/\1/p')"
	context_socket="$(docker context inspect "$context" --format '{{(index .Endpoints "docker").Host}}')"
	[ -n "$colima_socket" ] && [ "$context_socket" = "$colima_socket" ] || \
		die "Docker context $context does not point at Colima profile $PROFILE"
	log "selecting Docker context $context"
	docker context use "$context" >/dev/null
}

install_runsc_colima() {
	log "checking runsc in Colima profile $PROFILE"
	local force="$GVISOR_FORCE"
	colima ssh --profile "$PROFILE" -- sh -s "$GVISOR_VERSION" "$force" <<'EOF'
set -eu

version="$1"
force="$2"

if [ "$force" != "1" ] && \
   command -v runsc >/dev/null 2>&1 && \
   docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q '"runsc"'; then
	echo "runsc is already registered"
	exit 0
fi

if ! command -v wget >/dev/null 2>&1 || ! command -v sha512sum >/dev/null 2>&1; then
	sudo apt-get update
	sudo apt-get install -y --no-install-recommends ca-certificates wget coreutils
fi

arch="$(uname -m)"
case "$arch" in
	aarch64|x86_64) ;;
	*) echo "unsupported Colima architecture: $arch" >&2; exit 1 ;;
esac

work="$(mktemp -d)"
trap 'rm -rf "$work"' 0
base="https://storage.googleapis.com/gvisor/releases/release/${version}/${arch}"
wget -q -O "$work/runsc" "$base/runsc"
wget -q -O "$work/runsc.sha512" "$base/runsc.sha512"
(
	cd "$work"
	sha512sum -c runsc.sha512
)
sudo install -m 0755 "$work/runsc" /usr/local/bin/runsc
sudo /usr/local/bin/runsc install
sudo systemctl restart docker

attempt=0
while ! docker info >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	[ "$attempt" -lt 30 ] || { echo "Docker did not become ready after restart" >&2; exit 1; }
	sleep 1
done
docker info --format '{{json .Runtimes}}' | grep -q '"runsc"' || {
	echo "Docker does not expose runsc after installation" >&2
	exit 1
}
EOF
}

prepare_local_backend() {
	case "$(uname -s)" in
	Darwin)
		install_macos_dependency docker docker
		install_macos_dependency colima colima
		start_colima
		install_runsc_colima
		;;
	Linux)
		require_command docker
		if ! docker info >/dev/null 2>&1; then
			die "Docker daemon is not reachable"
		fi
		if ! docker info --format '{{json .Runtimes}}' | grep -q '"runsc"'; then
			die "Docker does not expose runsc; install and register gVisor before using --local"
		fi
		;;
	*)
		die "--local supports macOS with Colima or Linux with Docker"
		;;
	esac
}

install_cli() {
	log "building Nox"
	mkdir -p "$PREFIX"
	local binary
	binary="$(mktemp "${TMPDIR:-/tmp}/nox.XXXXXX")"
	BUILD_BINARY="$binary"
	TEMP_BINARY="$PREFIX/.nox.tmp.$$"
	(
		cd "$SOURCE_DIR"
		go build -o "$binary" ./cmd/nox
	)
	install -m 0755 "$binary" "$TEMP_BINARY"
	rm -f "$binary"
	BUILD_BINARY=""
	mv -f "$TEMP_BINARY" "$PREFIX/nox"
	TEMP_BINARY=""
	log "installed $PREFIX/nox"
}

build_runner_image() {
	require_command docker
	log "building runner image $IMAGE"
	docker build -t "$IMAGE" "$SOURCE_DIR/images/runner"
}

while (($# > 0)); do
	case "$1" in
	--local)
		LOCAL=1
		shift
		;;
	--prefix)
		(($# >= 2)) || die "--prefix requires a path"
		PREFIX="$2"
		shift 2
		;;
	--image)
		(($# >= 2)) || die "--image requires a tag"
		IMAGE="$2"
		shift 2
		;;
	--profile)
		(($# >= 2)) || die "--profile requires a name"
		PROFILE="$2"
		shift 2
		;;
	--gvisor-version)
		(($# >= 2)) || die "--gvisor-version requires a value"
		GVISOR_VERSION="$2"
		GVISOR_FORCE=1
		shift 2
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		die "unknown option: $1"
		;;
	esac
done

[[ -n "$PREFIX" ]] || die "install prefix cannot be empty"
[[ -n "$IMAGE" && "$IMAGE" != *[[:space:]]* ]] || die "image tag cannot be empty or contain whitespace"
[[ "$PROFILE" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid Colima profile: $PROFILE"
[[ "$COLIMA_CPUS" =~ ^[1-9][0-9]*$ ]] || die "Colima CPU count must be a positive integer"
[[ "$COLIMA_MEMORY" =~ ^[1-9][0-9]*(\.[0-9]+)?$ ]] || die "Colima memory must be a positive number"
[[ "$COLIMA_DISK" =~ ^[1-9][0-9]*$ ]] || die "Colima disk size must be a positive integer"
[[ -n "$GVISOR_VERSION" && "$GVISOR_VERSION" != *[[:space:]]* ]] || die "gVisor version cannot be empty or contain whitespace"

prepare_source
require_command go

if ((LOCAL)); then
	prepare_local_backend
	build_runner_image
fi

install_cli

if ((LOCAL)); then
	"$PREFIX/nox" doctor --image "$IMAGE"
else
	cat <<EOF

Nox is installed at $PREFIX/nox.
Run this installer again with --local to prepare the local Docker/gVisor
backend and runner image.
EOF
fi

case ":${PATH}:" in
*":${PREFIX}:"*) ;;
*) printf 'Add %s to PATH if it is not already present.\n' "$PREFIX" ;;
esac
