#!/bin/sh
# Footman installer: downloads a release binary, then runs `footman setup`.
#
#   sh install.sh               install (or update) and run setup
#   sh install.sh --uninstall   run `footman uninstall` (extra flags are passed on)
#
# Environment:
#   FOOTMAN_VERSION      release to install (default: latest)
#   FOOTMAN_INSTALL_DIR  where the binary goes (default: ~/.local/bin)
#   FOOTMAN_BASE_URL     where release files are downloaded from
#
# Anything after `--` is passed to `footman setup`, for example:
#   sh install.sh -- --non-interactive --name Alex
set -eu

# Placeholder until releases exist (S1 ships none yet).
REPO_URL="https://github.com/irobinson010/footman/releases"
INSTALL_DIR="${FOOTMAN_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

if [ -n "${FOOTMAN_BASE_URL:-}" ]; then
	BASE_URL="$FOOTMAN_BASE_URL"
elif [ -n "${FOOTMAN_VERSION:-}" ]; then
	BASE_URL="$REPO_URL/download/$FOOTMAN_VERSION"
else
	BASE_URL="$REPO_URL/latest/download"
fi

mode=install
case "${1:-}" in
	--uninstall) mode=uninstall; shift ;;
	-h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	--) shift ;;
	"") ;;
	*) die "unknown option $1 (try --help)" ;;
esac

# Uninstall works from the install receipt, so it needs only the binary.
if [ "$mode" = uninstall ]; then
	bin="$INSTALL_DIR/footman"
	[ -x "$bin" ] || bin=$(command -v footman 2>/dev/null) || die "footman is not installed"
	exec "$bin" uninstall "$@"
fi

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "unsupported OS $(uname -s); see the install docs" ;;
esac
case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) die "unsupported CPU $(uname -m)" ;;
esac
asset="footman-$os-$arch"

command -v curl >/dev/null 2>&1 || die "curl is required"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset from $BASE_URL"
curl -fsSL "$BASE_URL/$asset" -o "$tmp/footman" || die "download failed: $BASE_URL/$asset"
curl -fsSL "$BASE_URL/$asset.sha256" -o "$tmp/footman.sha256" || die "checksum download failed"

want=$(cut -d' ' -f1 "$tmp/footman.sha256")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/footman" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/footman" | cut -d' ' -f1)
else
	die "need sha256sum or shasum to verify the download"
fi
[ -n "$want" ] && [ "$want" = "$got" ] || die "checksum mismatch for $asset"

mkdir -p "$INSTALL_DIR"
chmod 755 "$tmp/footman"
mv "$tmp/footman" "$INSTALL_DIR/footman"
say "Installed $INSTALL_DIR/footman"

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) say "Note: $INSTALL_DIR is not on your PATH." ;;
esac

exec "$INSTALL_DIR/footman" setup "$@"
