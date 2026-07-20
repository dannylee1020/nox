#!/usr/bin/env bash
set -euo pipefail

# Install Nox from a checkout or source archive using an explicit local or
# remote installation profile.

INSTALL_PROFILE=""
SOURCE_DIR=""
SOURCE_TEMP=""
BUILD_BINARY=""
TEMP_BINARY=""
SKILL_TEMP=""
SKILL_BACKUP=""
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
PREFIX="${NOX_PREFIX:-}"
IMAGE="${NOX_RUNNER_IMAGE:-nox-runner:v0}"
COLIMA_PROFILE="${NOX_COLIMA_PROFILE:-nox}"
GVISOR_VERSION="${NOX_GVISOR_VERSION:-latest}"
GVISOR_FORCE="${NOX_GVISOR_FORCE_INSTALL:-0}"
if [ -n "${NOX_GVISOR_VERSION:-}" ]; then
	GVISOR_FORCE=1
fi
COLIMA_CPUS="${NOX_COLIMA_CPUS:-4}"
COLIMA_MEMORY="${NOX_COLIMA_MEMORY:-8}"
COLIMA_DISK="${NOX_COLIMA_DISK:-40}"
COLIMA_VM_TYPE="${NOX_COLIMA_VM_TYPE:-vz}"
CODEX_SKILLS_DIR="${NOX_CODEX_SKILLS_DIR:-${HOME}/.agents/skills}"
DESTDIR="${NOX_DESTDIR:-}"
REMOTE_PREFIX="/usr/local/bin"
REMOTE_USER="nox"
REMOTE_CONFIG_DIR="/etc/nox"
REMOTE_STATE_ROOT="/var/lib/nox"
REMOTE_SYSTEMD_DIR="/etc/systemd/system"

usage() {
	cat <<'EOF'
Usage: install.sh <local|remote> [options]

Install the Nox CLI from this checkout or a downloaded source archive.
Choose local for the interactive Codex workflow or remote for a Linux systemd worker.

Common options:
  --image IMAGE            runner image tag to build and validate
  -h, --help               show this help

Local options:
  --prefix PATH            install the binary under PATH
  --profile NAME           Colima profile name
  --gvisor-version VALUE   gVisor release path, default: latest
  --codex-skills-dir PATH  Codex skill root, default: ~/.agents/skills

Remote installation uses /usr/local/bin, /etc/nox, /var/lib/nox, and systemd.
It never enables or starts the service. NOX_DESTDIR can stage remote files for
packaging or isolated installer tests.

Environment overrides:
  NOX_PREFIX, NOX_RUNNER_IMAGE, NOX_COLIMA_PROFILE
  NOX_SOURCE_REPO, NOX_SOURCE_REF, NOX_SOURCE_ARCHIVE_URL
  NOX_GVISOR_VERSION, NOX_GVISOR_FORCE_INSTALL
  NOX_CODEX_SKILLS_DIR, NOX_DESTDIR
  NOX_COLIMA_CPUS, NOX_COLIMA_MEMORY, NOX_COLIMA_DISK
  NOX_COLIMA_VM_TYPE

Examples:
  curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- local
  curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- remote
  ./install.sh local --prefix "$HOME/.local/bin"
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

is_staged() {
	[ "$INSTALL_PROFILE" = "remote" ] && [ -n "$DESTDIR" ]
}

stage_path() {
	local path="$1"
	if [ -z "$DESTDIR" ] || [ "$DESTDIR" = "/" ]; then
		printf '%s' "$path"
	else
		printf '%s%s' "${DESTDIR%/}" "$path"
	fi
}

run_privileged() {
	if [ "$INSTALL_PROFILE" != "remote" ] || is_staged || [ "$(id -u)" -eq 0 ]; then
		"$@"
		return
	fi
	has_command sudo || die "remote installation needs root or sudo"
	sudo "$@"
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
		run_privileged rm -f "$TEMP_BINARY" || true
	fi
	if [ -n "$SKILL_TEMP" ]; then
		rm -rf "$SKILL_TEMP"
	fi
	if [ -n "$SKILL_BACKUP" ]; then
		rm -rf "$SKILL_BACKUP"
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

	if ! colima status "$COLIMA_PROFILE" >/dev/null 2>&1; then
		log "starting Colima profile $COLIMA_PROFILE"
		colima start "$COLIMA_PROFILE" \
			--runtime docker \
			--vm-type "$COLIMA_VM_TYPE" \
			--cpus "$COLIMA_CPUS" \
			--memory "$COLIMA_MEMORY" \
			--disk "$COLIMA_DISK"
	else
		log "reusing running Colima profile $COLIMA_PROFILE"
	fi

	local context="colima-${COLIMA_PROFILE}"
	docker context inspect "$context" >/dev/null 2>&1 || \
		die "Colima did not create Docker context $context"
	local status_json
	status_json="$(colima status "$COLIMA_PROFILE" --json)"
	printf '%s' "$status_json" | grep -q '"runtime":"docker"' || \
		die "Colima profile $COLIMA_PROFILE is not using the Docker runtime"
	local colima_socket
	local context_socket
	colima_socket="$(printf '%s' "$status_json" | sed -n 's/.*"docker_socket":"\([^"]*\)".*/\1/p')"
	context_socket="$(docker context inspect "$context" --format '{{(index .Endpoints "docker").Host}}')"
	[ -n "$colima_socket" ] && [ "$context_socket" = "$colima_socket" ] || \
		die "Docker context $context does not point at Colima profile $COLIMA_PROFILE"
	log "selecting Docker context $context"
	docker context use "$context" >/dev/null
}

install_runsc_colima() {
	log "checking runsc in Colima profile $COLIMA_PROFILE"
	local force="$GVISOR_FORCE"
	colima ssh --profile "$COLIMA_PROFILE" -- sh -s "$GVISOR_VERSION" "$force" <<'EOF'
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

run_docker() {
	if [ "$INSTALL_PROFILE" = "remote" ]; then
		run_privileged docker "$@"
	else
		docker "$@"
	fi
}

verify_docker_backend() {
	require_command docker
	if ! run_docker info >/dev/null 2>&1; then
		die "Docker daemon is not reachable"
	fi
	if ! run_docker info --format '{{json .Runtimes}}' | grep -q '"runsc"'; then
		die "Docker does not expose runsc; install and register gVisor before using Nox"
	fi
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
		verify_docker_backend
		;;
	*)
		die "the local backend supports macOS with Colima or Linux with Docker"
		;;
	esac
}

prepare_remote_backend() {
	[ "$(uname -s)" = "Linux" ] || die "the remote profile supports Linux only"
	verify_docker_backend
}

install_cli() {
	log "building Nox"
	local destination_prefix
	destination_prefix="$(stage_path "$PREFIX")"
	run_privileged mkdir -p "$destination_prefix"
	local binary
	binary="$(mktemp "${TMPDIR:-/tmp}/nox.XXXXXX")"
	BUILD_BINARY="$binary"
	TEMP_BINARY="$destination_prefix/.nox.tmp.$$"
	(
		cd "$SOURCE_DIR"
		go build -o "$binary" ./cmd/nox
	)
	run_privileged install -m 0755 "$binary" "$TEMP_BINARY"
	rm -f "$binary"
	BUILD_BINARY=""
	run_privileged mv -f "$TEMP_BINARY" "$destination_prefix/nox"
	TEMP_BINARY=""
	log "installed $PREFIX/nox"
}

preflight_codex_skill() {
	local source="$SOURCE_DIR/skills"
	local target="$CODEX_SKILLS_DIR/nox"
	[ -d "$source" ] || die "Nox Codex skill assets are missing from $SOURCE_DIR"
	[ -f "$source/SKILL.md" ] || die "Nox Codex skill is missing SKILL.md"
	[ -f "$source/references/cli.md" ] || die "Nox Codex skill is missing its CLI reference"
	[ -f "$source/references/task-contract.md" ] || die "Nox Codex skill is missing its task contract"
	if [ -e "$target" ] || [ -L "$target" ]; then
		[ -d "$target" ] && [ -f "$target/.nox-skill" ] && \
			grep -q '^format=1$' "$target/.nox-skill" || \
			die "refusing to overwrite user-managed Codex skill at $target"
	fi
}

install_codex_skill() {
	local source="$SOURCE_DIR/skills"
	local target="$CODEX_SKILLS_DIR/nox"
	local binary="$PREFIX/nox"
	preflight_codex_skill
	mkdir -p "$CODEX_SKILLS_DIR"

	SKILL_TEMP="$CODEX_SKILLS_DIR/.nox.tmp.$$"
	rm -rf "$SKILL_TEMP"
	mkdir -p "$SKILL_TEMP"
	cp -R "$source/." "$SKILL_TEMP/"
	rm -f "$SKILL_TEMP/references/installation.md" "$SKILL_TEMP/references/installation.md.tmpl"

	local escaped_binary="$binary"
	escaped_binary="${escaped_binary//\\/\\\\}"
	escaped_binary="${escaped_binary//&/\\&}"
	escaped_binary="${escaped_binary//|/\\|}"
	sed "s|__NOX_BINARY__|$escaped_binary|g" \
		"$source/references/installation.md.tmpl" > "$SKILL_TEMP/references/installation.md"
	printf 'format=1\nsource_ref=%s\nbinary=%s\n' "$SOURCE_REF" "$binary" > "$SKILL_TEMP/.nox-skill"
	chmod 0755 "$SKILL_TEMP" "$SKILL_TEMP/references" "$SKILL_TEMP/agents"
	chmod 0644 "$SKILL_TEMP/SKILL.md" "$SKILL_TEMP/agents/openai.yaml" \
		"$SKILL_TEMP/references/cli.md" "$SKILL_TEMP/references/task-contract.md" \
		"$SKILL_TEMP/references/installation.md" \
		"$SKILL_TEMP/.nox-skill"
	grep -q '^name: nox$' "$SKILL_TEMP/SKILL.md" || die "invalid installed Nox skill metadata"
	grep -Fq -- "$binary" "$SKILL_TEMP/references/installation.md" || \
		die "installed Nox skill does not reference $binary"

	if [ -e "$target" ] || [ -L "$target" ]; then
		SKILL_BACKUP="$CODEX_SKILLS_DIR/.nox.backup.$$"
		mv "$target" "$SKILL_BACKUP"
	fi
	if ! mv "$SKILL_TEMP" "$target"; then
		if [ -n "$SKILL_BACKUP" ]; then
			mv "$SKILL_BACKUP" "$target"
			SKILL_BACKUP=""
		fi
		die "install Nox Codex skill at $target"
	fi
	SKILL_TEMP=""
	if [ -n "$SKILL_BACKUP" ]; then
		rm -rf "$SKILL_BACKUP"
		SKILL_BACKUP=""
	fi
	log "installed Nox skill at $target"
}

build_runner_image() {
	require_command docker
	log "building runner image $IMAGE"
	run_docker build -t "$IMAGE" "$SOURCE_DIR/images/runner"
}

preflight_remote_install() {
	[ -f "$SOURCE_DIR/deploy/nox.service" ] || \
		die "remote service unit is missing from $SOURCE_DIR/deploy/nox.service"
}

ensure_remote_user() {
	is_staged && return
	if id "$REMOTE_USER" >/dev/null 2>&1; then
		log "reusing service user $REMOTE_USER"
	else
		require_command useradd
		log "creating service user $REMOTE_USER"
		run_privileged useradd --system --create-home --home-dir "$REMOTE_STATE_ROOT" "$REMOTE_USER"
	fi
	require_command usermod
	run_privileged usermod -aG docker "$REMOTE_USER"
}

install_remote_layout() {
	local config_dir state_root runs_dir codex_dir systemd_dir env_file
	config_dir="$(stage_path "$REMOTE_CONFIG_DIR")"
	state_root="$(stage_path "$REMOTE_STATE_ROOT")"
	runs_dir="$state_root/runs"
	codex_dir="$state_root/codex"
	systemd_dir="$(stage_path "$REMOTE_SYSTEMD_DIR")"
	env_file="$config_dir/nox.env"

	ensure_remote_user
	run_privileged mkdir -p "$config_dir" "$runs_dir" "$codex_dir" "$systemd_dir"
	run_privileged chmod 0750 "$config_dir" "$state_root" "$runs_dir"
	run_privileged chmod 0700 "$codex_dir"
	if ! is_staged; then
		run_privileged chown root:"$REMOTE_USER" "$config_dir"
		run_privileged chown "$REMOTE_USER:$REMOTE_USER" "$state_root" "$runs_dir" "$codex_dir"
	fi

	if [ -e "$env_file" ]; then
		log "preserving existing configuration $REMOTE_CONFIG_DIR/nox.env"
	else
		local template
		template="$(mktemp "${TMPDIR:-/tmp}/nox-env.XXXXXX")"
		cat >"$template" <<'EOF'
NOX_API_TOKEN=
NOX_GITHUB_TOKEN=
NOX_LISTEN_ADDR=0.0.0.0:8080
NOX_STATE_ROOT=/var/lib/nox
CODEX_HOME=/var/lib/nox/codex
NOX_GIT_NAME=Nox Worker
NOX_GIT_EMAIL=nox@localhost
EOF
		if is_staged; then
			install -m 0640 "$template" "$env_file"
		else
			run_privileged install -o root -g "$REMOTE_USER" -m 0640 "$template" "$env_file"
		fi
		rm -f "$template"
		log "created configuration template $REMOTE_CONFIG_DIR/nox.env"
	fi

	run_privileged install -m 0644 "$SOURCE_DIR/deploy/nox.service" "$systemd_dir/nox.service"
	log "installed systemd unit $REMOTE_SYSTEMD_DIR/nox.service"
}

run_doctor() {
	local binary="$(stage_path "$PREFIX")/nox"
	if [ "$INSTALL_PROFILE" = "remote" ]; then
		run_privileged "$binary" doctor --image "$IMAGE"
	else
		"$binary" doctor --image "$IMAGE"
	fi
}

print_local_summary() {
	cat <<EOF

Nox local CLI, Docker/gVisor backend, and Codex skill are installed.
CLI: $PREFIX/nox
Skill: $CODEX_SKILLS_DIR/nox
Restart Codex before using \$nox; local runs request scoped Docker access.
EOF

	case ":${PATH}:" in
	*":${PREFIX}:"*) ;;
	*) printf 'Add %s to PATH if it is not already present.\n' "$PREFIX" ;;
	esac
}

print_remote_summary() {
	cat <<EOF

Nox remote worker files are installed, but the service was not started.
CLI: $REMOTE_PREFIX/nox
Config: $REMOTE_CONFIG_DIR/nox.env
State: $REMOTE_STATE_ROOT

Next steps:
  1. Edit $REMOTE_CONFIG_DIR/nox.env and set NOX_API_TOKEN and NOX_GITHUB_TOKEN.
  2. Authenticate Codex for the service user:
     sudo -u $REMOTE_USER -H env HOME=$REMOTE_STATE_ROOT CODEX_HOME=$REMOTE_STATE_ROOT/codex codex login
  3. Review the private listen address and network firewall rules.
  4. Start after configuration:
     sudo systemctl daemon-reload
     sudo systemctl enable --now nox
EOF
}

install_local() {
	preflight_codex_skill
	prepare_local_backend
	build_runner_image
	install_cli
	install_codex_skill
	run_doctor
	print_local_summary
}

install_remote() {
	preflight_remote_install
	prepare_remote_backend
	build_runner_image
	install_cli
	install_remote_layout
	run_doctor
	print_remote_summary
}

PREFIX_EXPLICIT=0
if (($# == 0)); then
	die "an installation profile is required: local or remote"
fi
case "$1" in
local|remote)
	INSTALL_PROFILE="$1"
	shift
	;;
-h|--help)
	usage
	exit 0
	;;
*)
	die "unknown installation profile: $1 (expected local or remote)"
	;;
esac

while (($# > 0)); do
	case "$1" in
	--prefix)
		[ "$INSTALL_PROFILE" = "local" ] || die "--prefix is only supported by the local profile"
		(($# >= 2)) || die "--prefix requires a path"
		PREFIX="$2"
		PREFIX_EXPLICIT=1
		shift 2
		;;
	--image)
		(($# >= 2)) || die "--image requires a tag"
		IMAGE="$2"
		shift 2
		;;
	--profile)
		[ "$INSTALL_PROFILE" = "local" ] || die "--profile is only supported by the local profile"
		(($# >= 2)) || die "--profile requires a name"
		COLIMA_PROFILE="$2"
		shift 2
		;;
	--gvisor-version)
		[ "$INSTALL_PROFILE" = "local" ] || die "--gvisor-version is only supported by the local profile"
		(($# >= 2)) || die "--gvisor-version requires a value"
		GVISOR_VERSION="$2"
		GVISOR_FORCE=1
		shift 2
		;;
	--codex-skills-dir)
		[ "$INSTALL_PROFILE" = "local" ] || die "--codex-skills-dir is only supported by the local profile"
		(($# >= 2)) || die "--codex-skills-dir requires a path"
		CODEX_SKILLS_DIR="$2"
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

[[ -n "$IMAGE" && "$IMAGE" != *[[:space:]]* ]] || die "image tag cannot be empty or contain whitespace"

case "$INSTALL_PROFILE" in
local)
	if [ -z "$PREFIX" ]; then
		PREFIX="${HOME}/.local/bin"
	fi
	[ -z "$DESTDIR" ] || die "NOX_DESTDIR is only supported by the remote profile"
	[[ -n "$PREFIX" && "$PREFIX" != *[[:space:]]* ]] || die "install prefix cannot be empty or contain whitespace"
	[[ "$COLIMA_PROFILE" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid Colima profile: $COLIMA_PROFILE"
	[[ "$COLIMA_CPUS" =~ ^[1-9][0-9]*$ ]] || die "Colima CPU count must be a positive integer"
	[[ "$COLIMA_MEMORY" =~ ^[1-9][0-9]*(\.[0-9]+)?$ ]] || die "Colima memory must be a positive number"
	[[ "$COLIMA_DISK" =~ ^[1-9][0-9]*$ ]] || die "Colima disk size must be a positive integer"
	[[ -n "$GVISOR_VERSION" && "$GVISOR_VERSION" != *[[:space:]]* ]] || die "gVisor version cannot be empty or contain whitespace"
	[[ -n "$CODEX_SKILLS_DIR" && "$CODEX_SKILLS_DIR" != *[[:space:]]* ]] || die "Codex skill directory cannot be empty or contain whitespace"
	;;
remote)
	[ "$PREFIX_EXPLICIT" -eq 0 ] || die "the remote profile installs the CLI under $REMOTE_PREFIX"
	if [ -n "$PREFIX" ] && [ "$PREFIX" != "$REMOTE_PREFIX" ]; then
		die "NOX_PREFIX is only supported for the local profile"
	fi
	PREFIX="$REMOTE_PREFIX"
	[[ -z "$DESTDIR" || "$DESTDIR" = /* ]] || die "NOX_DESTDIR must be an absolute path"
	[[ "$DESTDIR" != *[[:space:]]* ]] || die "NOX_DESTDIR cannot contain whitespace"
	[ "$(uname -s)" = "Linux" ] || die "the remote profile supports Linux only"
	;;
esac

prepare_source
require_command go

case "$INSTALL_PROFILE" in
local)
	install_local
	;;
remote)
	install_remote
	;;
esac
