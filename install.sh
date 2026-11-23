#!/bin/sh
# ZAAFPulse installer for macOS on Apple Silicon and Linux (Ubuntu, Debian, RHEL, Rocky, AlmaLinux, Fedora,
# Amazon Linux, SUSE …) on x64 and arm64. One command:
#
#   curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh | sh
#
# Server (listen on the network, sign-in required; as root it becomes a systemd system service):
#
#   curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh | sudo sh -s -- --server
#
# Options: --server  --host ADDR  --port N  --version TAG  --no-service  --no-modify-path
#          --archive FILE [--sums FILE] (a local package, offline)  --verify-attestation (needs gh)
#          --rollback (back to the install this one replaced)  --uninstall [--purge]
# What it does: downloads the release package for this system (checksum verified), puts zaafpulse and
# zaafpulse-gw in the install folder, adds the `zaafpulse` command and starts it as a background service
# (launchd, or systemd). An earlier install (v1.x or v2) is kept for --rollback. Nothing else on the system
# is changed. Intel Macs, Alpine/musl, armv7l, ppc64le and s390x: use the v1.x installer it names.
set -eu

REPO="rifqifitriady/ZAAFPulse-deno-go"
V1_INSTALL="curl -fsSL https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/main/install.sh | sh"
VERSION=""; ARCHIVE=""; SUMS=""; HOST=""; PORT=""; SERVER=0; SERVICE=1; MODIFY_PATH=1; UNINSTALL=0; PURGE=0
ROLLBACK=0; ATTEST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER=1; HOST="${HOST:-0.0.0.0}" ;;
    --host) HOST="$2"; shift ;;
    --port) PORT="$2"; shift ;;
    --version) VERSION="$2"; shift ;;
    --archive) ARCHIVE="$2"; shift ;;
    --sums) SUMS="$2"; shift ;;
    --verify-attestation) ATTEST=1 ;;
    --rollback) ROLLBACK=1 ;;
    --no-service) SERVICE=0 ;;
    --no-modify-path) MODIFY_PATH=0 ;;
    --uninstall) UNINSTALL=1 ;;
    --purge) PURGE=1 ;;
    --source) echo "--source was removed in v2: build a package (deno task build --package) and use --archive" >&2; exit 2 ;;
    # The comment block above. Piped into sh there is no file to read ($0 is the shell), so it prints nothing.
    -h|--help) awk 'NR > 1 && !/^#/ { exit } NR > 1 { print }' "$0" 2>/dev/null || true; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mx\033[0m %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
fetch() { if have curl; then curl -fsSL --retry 3 "$1" -o "$2"; elif have wget; then wget -qO "$2" "$1"; else die "curl or wget is required"; fi; }
fetch_text() { if have curl; then curl -fsSL --retry 3 "$1"; else wget -qO- "$1"; fi; }
sha256() { if have sha256sum; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

OS="$(uname -s)"; ARCH="$(uname -m)"
case "$OS" in Darwin) PLAT=darwin ;; Linux) PLAT=linux ;; *) die "unsupported OS $OS (Windows: use install.ps1)" ;; esac

if [ "$(id -u)" = 0 ] && [ "$PLAT" = linux ]; then
  ROOT_INSTALL=1; PREFIX="/opt/zaafpulse"; BIN_DIR="/usr/local/bin"
else
  ROOT_INSTALL=0; PREFIX="${ZAAFPULSE_PREFIX:-$HOME/.local/share/zaafpulse}"; BIN_DIR="$HOME/.local/bin"
fi
LAUNCHER="$BIN_DIR/zaafpulse"
# The launcher wrapper holds the prefix in double quotes. Checked as given (before any folder is made), and again on
# the physical path, which a symlink may change.
check_prefix() {
  case "$PREFIX" in *'"'*|*'$'*|*'`'*|*'\'*) die "the install folder may not contain \", \$, \` or \\: $PREFIX" ;; esac
}
check_prefix
# The physical path: Deno matches the gateway's folder on PATH against the binary's own path.
resolve_prefix() { PREFIX="$(CDPATH= cd -- "$PREFIX" && pwd -P)"; check_prefix; }
# The physical path of a folder that may not exist yet: its nearest existing parent resolved, the rest appended.
phys() {
  if [ -d "$1" ]; then (CDPATH= cd -- "$1" && pwd -P)
  else printf '%s/%s\n' "$(phys "$(dirname -- "$1")")" "$(basename -- "$1")"; fi
}
# $PREFIX/bin is replaced and removed whole, so it must be the installers' own: not the folder of the zaafpulse command
# (ZAAFPULSE_PREFIX=$HOME/.local), and holding nothing but what an install puts there. Called on the resolved prefix.
guard_bin() {
  if [ "$PREFIX/bin" = "$(phys "$BIN_DIR")" ]; then
    printf '\033[1;31mx\033[0m %s\n' "the install folder's bin ($PREFIX/bin) is the command folder $BIN_DIR; choose another ZAAFPULSE_PREFIX" >&2
    return 1
  fi
  if [ -d "$PREFIX/bin" ]; then
    for f in "$PREFIX/bin"/* "$PREFIX/bin"/.[!.]* "$PREFIX/bin"/..?*; do
      [ -e "$f" ] || [ -L "$f" ] || continue
      case "${f##*/}" in zaafpulse|zaafpulse-gw|LICENSE) ;; *)
        printf '\033[1;31mx\033[0m %s\n' "$PREFIX/bin holds ${f##*/}, which no ZAAFPulse install puts there; it is not changed (move it, or choose another ZAAFPULSE_PREFIX)" >&2
        return 1 ;;
      esac
    done
  fi
}
# v1's app/ and node/ are the installers' only when v1's app/bin/zaafpulse.js is there: an install folder such as
# /usr/local may hold other folders of those names.
v1_here() { [ -f "$PREFIX/app/bin/zaafpulse.js" ]; }
warn_foreign_v1() {
  for d in app node; do
    if [ -e "$PREFIX/$d" ]; then warn "$PREFIX/$d was left alone: no ZAAFPulse v1 install ($PREFIX/app/bin/zaafpulse.js) is there"; fi
  done
}

# The service, through a given launcher: the old one before it is replaced, the restored one after a rollback.
service_stop() {
  if [ "$SERVICE" = 1 ] && [ -x "$1" ]; then
    if [ "$ROOT_INSTALL" = 1 ]; then "$1" service uninstall --system || true; else "$1" service uninstall || true; fi
  fi
}
service_start() {
  if [ "$SERVICE" = 1 ] && [ -x "$1" ]; then
    if [ "$ROOT_INSTALL" = 1 ]; then "$1" service install --system || warn "Service not installed; start it yourself with: zaafpulse start"
    else "$1" service install || warn "Service not installed; start it yourself with: zaafpulse start"; fi
  fi
}
# The AI clients' MCP entries, through a given launcher (v2's): an entry an earlier install registered starts that
# install's own files (v1's node and app/mcp.js, or v2's bin/zaafpulse), which an upgrade or a rollback moves away.
# `register-mcp registered` writes again every client that holds an entry, and no other. As root (a system install)
# the entries belong to the users who registered them, so each of them is told what to run instead.
reregister() {
  if [ "$ROOT_INSTALL" = 1 ]; then
    warn "AI clients registered for the earlier install start files it moved: each user who registered one runs: zaafpulse register-mcp registered"
  elif [ -x "$1" ]; then
    say "Registering the AI clients that had ZAAFPulse again"
    "$1" register-mcp registered || warn "Could not register the AI clients again; run: zaafpulse register-mcp registered"
  fi
}
# Move $1/<d> to $2/<d> for each folder d that exists in $1 and not yet in $2. From the install folder itself, app/ and
# node/ move only with v1's marker (v1_here, or a third argument v1 when the marker has moved already), and bin/ only
# after guard_bin.
move_dirs() {
  _v1="${3:-}"; if v1_here; then _v1=v1; fi   # once: moving app/ takes the marker along
  for d in bin app node; do
    if [ "$1" = "$PREFIX" ] && [ "$d" != bin ] && [ "$_v1" != v1 ]; then continue; fi
    if [ -e "$1/$d" ] && [ ! -e "$2/$d" ]; then mv "$1/$d" "$2/$d"; fi
  done
}
# The v2 zaafpulse command: the install folder first on PATH, so the binary may start its gateway.
write_launcher() {
  mkdir -p "$BIN_DIR"
  cat > "$LAUNCHER" <<EOF
#!/bin/sh
PATH="$PREFIX/bin:\$PATH"; export PATH
exec "$PREFIX/bin/zaafpulse" "\$@"
EOF
  chmod 755 "$LAUNCHER"
}

if [ "$UNINSTALL" = 1 ]; then
  if [ -d "$PREFIX" ]; then resolve_prefix; guard_bin || exit 1; fi
  service_stop "$LAUNCHER"
  # Only what the installers put there: on Linux the data folder sits inside the default prefix (…/zaafpulse/data).
  if v1_here; then rm -rf "${PREFIX:?}/app" "${PREFIX:?}/node"; else warn_foreign_v1; fi
  for d in app.new app.old bin bin.new rollback rollback.new rollforward; do rm -rf "${PREFIX:?}/$d"; done
  rm -f "$LAUNCHER"; rmdir "$PREFIX" 2>/dev/null || true
  say "ZAAFPulse removed from $PREFIX."
  if [ "$PURGE" = 1 ]; then
    if [ "$ROOT_INSTALL" = 1 ]; then rm -rf /var/lib/zaafpulse "$PREFIX"; userdel zaafpulse 2>/dev/null || true
    elif [ "$PLAT" = darwin ]; then rm -rf "$HOME/Library/Application Support/ZAAFPulse"
    else rm -rf "${XDG_DATA_HOME:-$HOME/.local/share}/zaafpulse"; fi
    say "Data removed too."
  else
    say "Your data (config, keys, history) was kept. Add --purge to delete it."
  fi
  exit 0
fi

if [ "$ROLLBACK" = 1 ]; then
  [ -f "$PREFIX/rollback/VERSION" ] || die "nothing to roll back to: $PREFIX/rollback holds no saved install"
  resolve_prefix
  guard_bin || exit 1
  RB="$PREFIX/rollback"; RF="$PREFIX/rollforward"
  service_stop "$LAUNCHER"
  rm -rf "$RF"; mkdir -p "$RF"
  move_dirs "$PREFIX" "$RF"
  if [ -f "$LAUNCHER" ]; then cp "$LAUNCHER" "$RF/launcher"; fi
  move_dirs "$RB" "$PREFIX"
  if [ -f "$RB/launcher" ]; then
    mkdir -p "$BIN_DIR"; cp "$RB/launcher" "$LAUNCHER"; chmod 755 "$LAUNCHER"
  elif [ -f "$PREFIX/bin/zaafpulse" ]; then
    # It was installed without a launcher (removed by hand): a v2 generation gets a fresh one.
    write_launcher
  else
    rm -f "$LAUNCHER"
    warn "No zaafpulse command was saved with this install; run it with: \"$PREFIX/node/bin/node\" \"$PREFIX/app/bin/zaafpulse.js\""
  fi
  OLD="$(head -n1 "$RB/VERSION" 2>/dev/null || true)"
  rm -rf "$RB"
  service_start "$LAUNCHER"
  # The AI clients' entries start the install that was just moved to rollforward/.
  if [ -f "$PREFIX/bin/zaafpulse" ]; then reregister "$LAUNCHER"
  else warn "AI clients registered by v2 start files now in $RF: register them again with the restored version (zaafpulse register-mcp <client>)"; fi
  say "Rolled back: ZAAFPulse ${OLD:-unknown} is active again. The install it replaced is in $RF."
  exit 0
fi

# ---- platform: the v2 builds ----
case "$ARCH" in
  x86_64|amd64) NARCH=x64 ;;
  aarch64|arm64) NARCH=arm64 ;;
  armv7l|ppc64le|s390x) die "no v2 build for $PLAT-$ARCH; the v1.x installer still supports it: $V1_INSTALL" ;;
  *) die "unsupported CPU $ARCH" ;;
esac
# v2 has no Intel Mac build (five targets, owner 2026-11-23). A shell under Rosetta on Apple Silicon also reports
# x86_64: sysctl tells the two apart, and there the arm64 build runs natively.
if [ "$PLAT" = darwin ] && [ "$NARCH" = x64 ]; then
  if [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then NARCH=arm64
  else die "no v2 build for Intel Macs (darwin-x64); the v1.x installer still supports them: $V1_INSTALL"; fi
fi
if [ "$PLAT" = linux ] && { [ -f /etc/alpine-release ] || (ldd --version 2>&1 | grep -qi musl); }; then
  die "no v2 build for $PLAT-musl-$NARCH; the v1.x installer still supports it: $V1_INSTALL"
fi

# ---- the package: a local one, or the release's (checksum verified) ----
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
if [ -n "$ARCHIVE" ]; then
  [ -f "$ARCHIVE" ] || die "no such archive: $ARCHIVE"
  PKG="$ARCHIVE"; NAME="$(basename "$ARCHIVE")"
  if [ -n "$SUMS" ]; then cp "$SUMS" "$TMP/SHA256SUMS.txt" || die "cannot read $SUMS"
  else warn "$NAME is not checksum-verified (local archive)"; fi
else
  have curl || have wget || die "curl or wget is required"
  if [ -z "$VERSION" ]; then
    VERSION="$(fetch_text "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1 || true)"
    [ -n "$VERSION" ] || die "could not find the latest release on GitHub; try again, or name one with --version vX.Y.Z"
  fi
  case "$VERSION" in main|master) die "v2 installs releases only; name one with --version vX.Y.Z" ;; esac
  NAME="zaafpulse-${VERSION#v}-$PLAT-$NARCH.tar.gz"
  REL="https://github.com/$REPO/releases/download/$VERSION"
  say "Downloading ZAAFPulse $VERSION ($PLAT-$NARCH)"
  fetch "$REL/SHA256SUMS.txt" "$TMP/SHA256SUMS.txt" 2>/dev/null || die "could not get SHA256SUMS.txt for $VERSION; nothing was installed"
  PKG="$TMP/$NAME"
  fetch "$REL/$NAME" "$PKG" || die "could not download $NAME; nothing was installed"
fi
if [ -f "$TMP/SHA256SUMS.txt" ]; then
  WANT="$(grep "  $NAME\$" "$TMP/SHA256SUMS.txt" | head -n1 | cut -d' ' -f1 || true)"
  [ -n "$WANT" ] || die "no checksum for $NAME in SHA256SUMS.txt; nothing was installed"
  [ "$(sha256 "$PKG")" = "$WANT" ] || die "checksum mismatch for $NAME: the download was changed or damaged; nothing was installed"
  say "Checksum verified ($NAME)"
fi
if [ "$ATTEST" = 1 ]; then
  have gh || die "--verify-attestation needs the GitHub CLI (gh); nothing was installed"
  gh attestation verify "$PKG" --repo "$REPO" || die "the attestation check failed for $NAME; nothing was installed"
  say "Attestation verified ($NAME)"
fi

mkdir "$TMP/x"; tar -xzf "$PKG" -C "$TMP/x" || die "could not unpack $NAME; nothing was installed"
SRC="$(find "$TMP/x" -mindepth 1 -maxdepth 1 -type d | head -n1)"
for f in zaafpulse zaafpulse-gw; do
  [ -n "$SRC" ] && [ -f "$SRC/$f" ] && [ -x "$SRC/$f" ] || die "$NAME holds no executable $f; nothing was installed"
  # A browser download carries the quarantine flag, and Gatekeeper would stop the binary.
  if [ "$PLAT" = darwin ]; then xattr -d com.apple.quarantine "$SRC/$f" 2>/dev/null || true; fi
done

# Before anything is changed, the new binary must run on this system.
NEW_PREFIX=0; [ -d "$PREFIX" ] || NEW_PREFIX=1
mkdir -p "$PREFIX"; resolve_prefix
guard_bin || { if [ "$NEW_PREFIX" = 1 ]; then rmdir "$PREFIX" 2>/dev/null || true; fi; die "nothing was changed"; }
rm -rf "$PREFIX/bin.new"; mkdir "$PREFIX/bin.new"
cp "$SRC/zaafpulse" "$SRC/zaafpulse-gw" "$PREFIX/bin.new/"
if [ -f "$SRC/LICENSE" ]; then cp "$SRC/LICENSE" "$PREFIX/bin.new/"; fi
NEWV="$("$PREFIX/bin.new/zaafpulse" version 2>/dev/null)" || {
  rm -rf "$PREFIX/bin.new"; if [ "$NEW_PREFIX" = 1 ]; then rmdir "$PREFIX" 2>/dev/null || true; fi
  die "the new zaafpulse does not run on this system; nothing was changed"
}
say "New version runs: $(printf '%s\n' "$NEWV" | head -n1)"

# An upgrade cut off while it saved the old install leaves rollback.new. With its VERSION written and no install left
# in place, it is the whole old install: finish the move and keep it as the rollback. Otherwise put back what it holds.
if [ -d "$PREFIX/rollback.new" ]; then
  if [ -f "$PREFIX/rollback.new/VERSION" ] && [ ! -f "$PREFIX/app/bin/zaafpulse.js" ] && [ ! -f "$PREFIX/bin/zaafpulse" ]; then
    if [ -f "$PREFIX/rollback.new/app/bin/zaafpulse.js" ]; then move_dirs "$PREFIX" "$PREFIX/rollback.new" v1
    else move_dirs "$PREFIX" "$PREFIX/rollback.new"; fi
    rm -rf "$PREFIX/rollback"; mv "$PREFIX/rollback.new" "$PREFIX/rollback"
    warn "An earlier upgrade was cut off; the install it saved is in $PREFIX/rollback."
  else
    move_dirs "$PREFIX/rollback.new" "$PREFIX"
    rm -rf "$PREFIX/rollback.new"
  fi
fi

# ---- the current install: v1 (app/, run by node) or v2 (bin/) ----
OLD_KIND=""; OLD_VERSION=""
if [ -f "$PREFIX/app/bin/zaafpulse.js" ]; then
  OLD_KIND=v1
  OLD_VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$PREFIX/app/package.json" 2>/dev/null | head -n1 || true)"
elif [ -f "$PREFIX/bin/zaafpulse" ]; then
  OLD_KIND=v2
  OLD_VERSION="$("$PREFIX/bin/zaafpulse" version 2>/dev/null | head -n1 || true)"
fi
case "$OLD_VERSION" in [0-9]*.[0-9]*) OLD_TAG="v$OLD_VERSION" ;; *) OLD_TAG=unknown ;; esac

# A re-run at the version already installed replaces only bin/: the saved rollback stays the install before it.
SAME=0
if [ "$OLD_KIND" = v2 ] && [ -n "$OLD_VERSION" ] && [ "$OLD_VERSION" = "$(printf '%s\n' "$NEWV" | head -n1)" ]; then SAME=1; fi
if [ "$SAME" = 1 ]; then
  service_stop "$LAUNCHER"
  say "Reinstalling $OLD_TAG; the saved rollback (if any) is kept."
elif [ -n "$OLD_KIND" ]; then
  # Stop the old service through the old launcher, before it is replaced.
  service_stop "$LAUNCHER"
  # One generation for --rollback: the folders it ran from, and its launcher.
  # VERSION first: a rollback.new with it is known to hold the old install (see above).
  rm -rf "$PREFIX/rollback.new"; mkdir "$PREFIX/rollback.new"
  printf '%s\n' "$OLD_TAG" > "$PREFIX/rollback.new/VERSION"
  if [ -f "$LAUNCHER" ]; then cp "$LAUNCHER" "$PREFIX/rollback.new/launcher"; fi
  move_dirs "$PREFIX" "$PREFIX/rollback.new"
  if [ "$OLD_KIND" = v2 ]; then warn_foreign_v1; fi
  rm -rf "$PREFIX/rollback"; mv "$PREFIX/rollback.new" "$PREFIX/rollback"
  say "The $OLD_KIND install ($OLD_TAG) was saved in $PREFIX/rollback: --rollback brings it back."
fi
rm -rf "$PREFIX/bin"; mv "$PREFIX/bin.new" "$PREFIX/bin"
# A rollback's leftover: this install replaces it.
rm -rf "$PREFIX/rollforward"
if [ "$ROOT_INSTALL" = 1 ]; then chmod -R a+rX "$PREFIX"; fi

# ---- the zaafpulse command ----
write_launcher
say "Command installed: $LAUNCHER"
if [ "$ROOT_INSTALL" = 0 ] && [ "$MODIFY_PATH" = 1 ]; then
  case ":$PATH:" in *":$BIN_DIR:"*) ;; *)
    for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
      if [ -f "$rc" ] || [ "$rc" = "$HOME/.profile" ]; then
        grep -qs 'zaafpulse' "$rc" || printf '\n# ZAAFPulse\nexport PATH="$HOME/.local/bin:$PATH"\n' >> "$rc"
      fi
    done
    say "Added ~/.local/bin to PATH in your shell profile (open a new terminal, or: export PATH=\"\$HOME/.local/bin:\$PATH\")" ;;
  esac
fi

# ---- server mode: an admin password before the first start ----
if [ -n "$HOST" ]; then export ZAAFPULSE_HOST="$HOST"; fi
if [ -n "$PORT" ]; then export ZAAFPULSE_PORT="$PORT"; fi
if [ -n "$HOST" ] && [ "$HOST" != 127.0.0.1 ] && [ "$HOST" != localhost ]; then
  if [ -n "${ZAAFPULSE_ADMIN_PASSWORD:-}" ]; then
    printf '%s\n' "$ZAAFPULSE_ADMIN_PASSWORD" | "$LAUNCHER" set-password
  elif [ -r /dev/tty ]; then
    say "Server mode: choose the dashboard admin password"
    "$LAUNCHER" set-password < /dev/tty
  else
    warn "Server mode needs an admin password: run \`zaafpulse set-password\`, then \`zaafpulse service restart\`."
  fi
fi

# ---- background service ----
service_start "$LAUNCHER"

# ---- the AI clients' MCP entries: an upgrade moved the files the earlier install's entries start ----
if [ -n "$OLD_KIND" ] && [ "$SAME" = 0 ]; then reregister "$LAUNCHER"; fi

NEW_VERSION="$("$LAUNCHER" version 2>/dev/null | head -n1 || true)"
P="${PORT:-4777}"
echo
say "ZAAFPulse ${NEW_VERSION:-} is installed."
if [ -n "$HOST" ] && [ "$HOST" != 127.0.0.1 ]; then
  echo "    Dashboard:  http://$(uname -n):$P   (sign in with the admin password)"
  if have firewall-cmd; then echo "    Firewall:   sudo firewall-cmd --add-port=$P/tcp --permanent && sudo firewall-cmd --reload"; fi
  if have ufw; then echo "    Firewall:   sudo ufw allow $P/tcp"; fi
  echo "    Use HTTPS in production: a reverse proxy, or ZAAFPULSE_TLS_CERT and ZAAFPULSE_TLS_KEY."
else
  echo "    Dashboard:  http://localhost:$P"
fi
echo "    Commands:   zaafpulse status | doctor | open | service restart | help"
if [ -f "$PREFIX/rollback/VERSION" ]; then
  echo "    Roll back:  curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sh -s -- --rollback"
fi
echo "    Uninstall:  curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sh -s -- --uninstall"
if [ -n "$OLD_KIND" ] && [ "$SERVICE" = 0 ]; then
  warn "--no-service: the service of the earlier install (if any) was left running, on the old version. Switch it with: zaafpulse service install"
fi
