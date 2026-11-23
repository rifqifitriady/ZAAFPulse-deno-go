#!/bin/sh
# The install test of one release package on macOS or Linux (P6 Decision 14; release.yml job install-test):
#
#   sh scripts/install-test.sh --archive FILE --sums FILE --name <target name> [--no-service-test] [--no-upgrade]
#                              [--m12 | --no-m12]
#
# Everything runs in a temp folder: HOME, the install prefix (ZAAFPULSE_PREFIX), the data folders. The server listens
# on 127.0.0.1:4877 (never 4777), with ZAAFPULSE_AUTH=none. A server this script starts runs in the background through
# the installed `zaafpulse` command (the launcher puts the install folder first on PATH, P6 Decision 4) and is always
# stopped, on failure too.
#   1. Clean install: install.sh --archive --sums --no-modify-path; `zaafpulse version` is the package version and
#      `zaafpulse doctor` passes; the server answers /login.html and /api/gateway/status is front and ready with the
#      package version (gateway.engine "front" is written first); then uninstall, and the port is free.
#      On macOS the install includes the launchd service (the service is what answers, and --uninstall removes it).
#      That runs only on a CI runner (GITHUB_ACTIONS=true): the launchd label is the owner's live one, so on a
#      developer's machine pass --no-service-test. Linux runners have no user systemd session: --no-service and a
#      background start there.
#   2. Upgrade from v1.1.0: the v1.1.0 installer from the public repo at its tag (--no-service, needs Node 18+ on PATH)
#      into the same prefix; v1 starts and a marker setting is written (userName, POST /api/config); the v2 installer
#      from the package; v2 starts in front mode and sees the marker, and rollback/VERSION says v1.1.0; --rollback; v1
#      starts again and still sees the marker. --no-upgrade skips it.
#   3. M12: scripts/upgrade-check.js --v2 on the unpacked package (needs Node). By default on linux-x64 only (the
#      Windows side is install-test.ps1 on win-x64); --m12 / --no-m12 force it.
# Prints one summary line per part (also to $GITHUB_STEP_SUMMARY when set) and exits non-zero when a part fails.
# Set KEEP_TEMP=1 to keep the temp folder.
set -eu

PORT=4877
V1_TAG=v1.1.0
V1_INSTALLER="https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/$V1_TAG/install.sh"

ARCHIVE=""; SUMS=""; NAME=""; SERVICE_TEST=1; UPGRADE=1; M12=auto
while [ $# -gt 0 ]; do
  case "$1" in
    --archive) ARCHIVE="${2:-}"; shift ;;
    --sums) SUMS="${2:-}"; shift ;;
    --name) NAME="${2:-}"; shift ;;
    --no-service-test) SERVICE_TEST=0 ;;
    --no-upgrade) UPGRADE=0 ;;
    --m12) M12=1 ;;
    --no-m12) M12=0 ;;
    -h|--help) awk 'NR > 1 && !/^#/ { exit } NR > 1 { print }' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*"; }
die() { printf 'x %s\n' "$*" >&2; exit 1; }

[ -n "$ARCHIVE" ] && [ -n "$SUMS" ] && [ -n "$NAME" ] || die "usage: install-test.sh --archive FILE --sums FILE --name <target name>"
[ -f "$ARCHIVE" ] || die "no such archive: $ARCHIVE"
[ -f "$SUMS" ] || die "no such sums file: $SUMS"
command -v curl >/dev/null 2>&1 || die "curl is required"

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
ARCHIVE="$(CDPATH= cd -- "$(dirname -- "$ARCHIVE")" && pwd -P)/$(basename -- "$ARCHIVE")"
SUMS="$(CDPATH= cd -- "$(dirname -- "$SUMS")" && pwd -P)/$(basename -- "$SUMS")"
BASE="$(basename -- "$ARCHIVE")"
case "$BASE" in
  zaafpulse-*-"$NAME".tar.gz) ;;
  *) die "$BASE is not the tar.gz package of $NAME (zaafpulse-<version>-$NAME.tar.gz)" ;;
esac
VERSION="${BASE#zaafpulse-}"; VERSION="${VERSION%-"$NAME".tar.gz}"
TOP="${BASE%.tar.gz}"

case "$(uname -s)" in
  Darwin) PLAT=darwin ;;
  Linux) PLAT=linux; SERVICE_TEST=0 ;;
  *) die "install-test.sh is for macOS and Linux; on Windows use install-test.ps1" ;;
esac
if [ "$SERVICE_TEST" = 1 ] && [ "${GITHUB_ACTIONS:-}" != true ]; then
  die "the service test runs on a CI runner only (it uses the owner's launchd label); pass --no-service-test here"
fi
if [ "$M12" = auto ]; then
  if [ "$NAME" = linux-x64 ]; then M12=1; else M12=0; fi
fi

# The port must be free: never fight another server (or the owner's).
if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$PORT/"; then die "port $PORT is in use; stop what listens there first"; fi

# ---- the temp world ----
T="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/zp-install-test.XXXXXX")"
T="$(CDPATH= cd -- "$T" && pwd -P)"
export HOME="$T/home"
export ZAAFPULSE_PREFIX="$T/prefix"
export XDG_CONFIG_HOME="$HOME/.config" XDG_DATA_HOME="$HOME/.local/share" XDG_CACHE_HOME="$HOME/.cache"
export ZAAFPULSE_PORT="$PORT" ZAAFPULSE_AUTH=none
unset ZAAFPULSE_HOST ZAAFPULSE_DATA ZAAFPULSE_PUBLIC_URL ZAAFPULSE_SECRET_STORE CLAUDE_CONFIG_DIR ZAAFPULSE_CLAUDE_DIRS \
  CODEX_HOME ZAAFPULSE_CLAUDE_BIN 2>/dev/null || true
mkdir -p "$HOME" "$T/logs"
LAUNCHER="$HOME/.local/bin/zaafpulse"
PIDFILE="$T/server.pid"
SERVICE_FLAG="$T/service.installed"
URL="http://127.0.0.1:$PORT"
SUMMARY="$T/summary.txt"; : > "$SUMMARY"

# ---- helpers ----
# Waits up to $1 seconds for the command in the rest of the arguments to succeed.
wait_for() {
  n="$1"; shift
  end=$(( $(date +%s) + n ))
  while :; do
    if "$@" >/dev/null 2>&1; then return 0; fi
    [ "$(date +%s)" -lt "$end" ] || return 1
    sleep 1
  done
}
login_ok() { [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$URL/login.html")" = 200 ]; }
# curl's exit code 7: the connection was refused, so nothing listens.
port_free() { curl -s -o /dev/null --max-time 2 "$URL/" ; [ $? -eq 7 ]; }
gateway_status() { curl -s --max-time 3 "$URL/api/gateway/status"; }
gateway_ready() {
  s="$(gateway_status)" || return 1
  printf '%s' "$s" | grep -q '"mode":"front"' && printf '%s' "$s" | grep -q '"state":"ready"'
}
json_field() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" | head -n1; }
# config.json of a data folder: written before the first start (no price download; the gateway in front when asked).
write_config() {
  mkdir -p "$1"
  if [ "$2" = front ]; then
    printf '%s\n' '{ "gateway": { "engine": "front" }, "pricingSync": { "enabled": false } }' > "$1/config.json"
  else
    printf '%s\n' '{ "pricingSync": { "enabled": false } }' > "$1/config.json"
  fi
}
# Sets gateway.engine "front" in a config.json that v1 wrote to (keeps every other key). Needs Node.
set_front() {
  node -e '
    const fs = require("fs"); const f = process.argv[1];
    const c = JSON.parse(fs.readFileSync(f, "utf8"));
    c.gateway = { ...(c.gateway || {}), engine: "front" };
    fs.writeFileSync(f, JSON.stringify(c, null, 2) + "\n");' "$1/config.json"
}
# Starts the server in the background through the launcher; its pid (the binary or node: the launcher execs) goes to
# $PIDFILE so it is stopped whatever happens.
start_bg() {
  [ -x "$LAUNCHER" ] || die "no launcher at $LAUNCHER"
  "$LAUNCHER" start > "$T/logs/$1.log" 2>&1 &
  echo $! > "$PIDFILE"
  if ! wait_for 60 login_ok; then
    tail -n 40 "$T/logs/$1.log" >&2 || true
    die "$1: no 200 on /login.html within 60 s"
  fi
}
stop_bg() {
  [ -f "$PIDFILE" ] || return 0
  pid="$(cat "$PIDFILE")"; rm -f "$PIDFILE"
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    i=0
    while kill -0 "$pid" 2>/dev/null && [ $i -lt 15 ]; do sleep 1; i=$((i + 1)); done
    if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
  fi
  wait_for 20 port_free || die "port $PORT still in use 20 s after the stop"
}
# The marker setting: userName, which v1 and v2 both keep in config.json.
MARKER="install-test-$$-$(date +%s)"
write_marker() {
  curl -fsS --max-time 10 -H 'content-type: application/json' -d "{\"userName\":\"$MARKER\"}" "$URL/api/config" > /dev/null \
    || die "$1: POST /api/config failed"
}
check_marker() {
  got="$(curl -fsS --max-time 10 "$URL/api/config" | json_field userName)" || die "$1: GET /api/config failed"
  [ "$got" = "$MARKER" ] || die "$1: userName is '$got', not the marker '$MARKER'"
}
check_front() {
  if ! wait_for 60 gateway_ready; then
    printf '%s\n' "$(gateway_status)" >&2
    die "$1: /api/gateway/status is not front and ready within 60 s"
  fi
  gv="$(gateway_status | json_field gwVersion)"
  [ "$gv" = "$VERSION" ] || die "$1: gwVersion '$gv', package $VERSION"
}
check_version() {
  v="$("$LAUNCHER" version 2>&1 | head -n1)" || die "$2: zaafpulse version failed: $v"
  [ "$v" = "$1" ] || die "$2: zaafpulse version says '$v', expected $1"
}
v2_install() { sh "$ROOT/install.sh" --archive "$ARCHIVE" --sums "$SUMS" --no-modify-path --port "$PORT" "$@"; }
service_uninstall() {
  if [ -f "$SERVICE_FLAG" ] && [ -x "$LAUNCHER" ]; then "$LAUNCHER" service uninstall || true; fi
  rm -f "$SERVICE_FLAG"
}
# After a part, pass or fail: nothing of it may keep running. In a subshell, so a failed stop (die) cannot end the
# caller, which may be the EXIT trap.
after_part() {
  ( stop_bg ) || echo "! a server of this test may still hold port $PORT" >&2
  ( service_uninstall ) || true
}
cleanup() {
  code=$?
  after_part
  if [ "${KEEP_TEMP:-0}" = 1 ]; then echo "temp folder kept: $T"; else rm -rf "$T"; fi
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# ---- 1. clean install ----
part_clean() {
  D="$T/data-clean"; write_config "$D" front; export ZAAFPULSE_DATA="$D"
  if [ "$SERVICE_TEST" = 1 ]; then
    touch "$SERVICE_FLAG"
    v2_install
    HOW="launchd service"
  else
    v2_install --no-service
    HOW="background start"
  fi
  check_version "$VERSION" "clean install"
  "$LAUNCHER" doctor > "$T/logs/doctor.log" 2>&1 || { cat "$T/logs/doctor.log" >&2; die "clean install: zaafpulse doctor failed"; }
  if [ "$SERVICE_TEST" = 1 ]; then
    if ! wait_for 60 login_ok; then
      "$LAUNCHER" service status >&2 || true
      tail -n 40 "$HOME/Library/Logs/ZAAFPulse/server.log" >&2 2>/dev/null || true
      die "clean install: the service does not answer /login.html within 60 s"
    fi
    "$LAUNCHER" service status || true
  else
    start_bg clean
  fi
  check_front "clean install"
  if [ "$SERVICE_TEST" = 1 ]; then
    sh "$ROOT/install.sh" --uninstall
    rm -f "$SERVICE_FLAG"
    wait_for 20 port_free || die "clean install: port $PORT still in use 20 s after the uninstall"
  else
    stop_bg
    sh "$ROOT/install.sh" --uninstall --no-service
  fi
  [ ! -e "$LAUNCHER" ] || die "clean install: the launcher is still there after --uninstall"
  [ ! -e "$ZAAFPULSE_PREFIX/bin" ] || die "clean install: $ZAAFPULSE_PREFIX/bin is still there after --uninstall"
  echo "clean install: PASS ($HOW; version $VERSION; doctor passes; gateway in front, ready, gwVersion $VERSION; uninstalled)" > "$T/part.txt"
}

# ---- 2. upgrade from v1.1.0 and roll back ----
part_upgrade() {
  command -v node >/dev/null 2>&1 || die "upgrade: the v1.1.0 installer needs Node 18+ on PATH"
  D="$T/data-upgrade"; write_config "$D" js; export ZAAFPULSE_DATA="$D"
  curl -fsSL --retry 3 "$V1_INSTALLER" -o "$T/install-v1.sh" || die "upgrade: could not download $V1_INSTALLER"
  sh "$T/install-v1.sh" --no-service --no-modify-path --version "$V1_TAG"
  grep -q 'app/bin/zaafpulse.js' "$LAUNCHER" || die "upgrade: the launcher is not v1's"
  start_bg v1-before
  write_marker "v1"
  check_marker "v1"
  stop_bg

  set_front "$D"
  v2_install --no-service
  [ "$(head -n1 "$ZAAFPULSE_PREFIX/rollback/VERSION" 2>/dev/null)" = "$V1_TAG" ] \
    || die "upgrade: rollback/VERSION is '$(cat "$ZAAFPULSE_PREFIX/rollback/VERSION" 2>/dev/null)', not $V1_TAG"
  check_version "$VERSION" "upgrade"
  start_bg v2
  check_front "upgrade (v2)"
  check_marker "upgrade (v2)"
  stop_bg

  sh "$ROOT/install.sh" --rollback --no-service
  grep -q 'app/bin/zaafpulse.js' "$LAUNCHER" || die "rollback: the launcher is not v1's again"
  check_version "${V1_TAG#v}" "rollback"
  start_bg v1-after
  check_marker "rollback (v1)"
  stop_bg
  sh "$ROOT/install.sh" --uninstall --no-service
  echo "upgrade: PASS ($V1_TAG -> $VERSION: marker kept, rollback/VERSION $V1_TAG; --rollback -> $V1_TAG: marker kept)" > "$T/part.txt"
}

# ---- 3. M12 ----
part_m12() {
  command -v node >/dev/null 2>&1 || die "M12: needs Node on PATH"
  mkdir -p "$T/m12"
  tar -xzf "$ARCHIVE" -C "$T/m12"
  node "$ROOT/scripts/upgrade-check.js" --v2 "$T/m12/$TOP/zaafpulse" | tee "$T/logs/m12.json"
  echo "M12: PASS ($(cut -c1-300 "$T/logs/m12.json"))" > "$T/part.txt"
}

# Runs one part in a subshell with set -e, so its first failed check ends it; the next part still runs.
FAILED=0
run_part() {
  label="$1"; fn="$2"
  rm -f "$T/part.txt"
  say "$NAME: $label"
  set +e
  ( set -e; "$fn" )
  rc=$?
  set -e
  after_part
  if [ $rc -eq 0 ] && [ -f "$T/part.txt" ]; then
    cat "$T/part.txt" >> "$SUMMARY"
  else
    FAILED=1
    echo "$label: FAIL (exit $rc; see the log above)" >> "$SUMMARY"
    for f in "$T"/logs/*.log; do
      [ -f "$f" ] || continue
      echo "--- last lines of $(basename "$f")" >&2; tail -n 30 "$f" >&2
    done
  fi
  rm -f "$T"/logs/*.log
}

say "install test of $BASE (version $VERSION) on $PLAT, port $PORT, temp $T"
run_part "clean install" part_clean
if [ "$UPGRADE" = 1 ]; then run_part "upgrade" part_upgrade; else echo "upgrade: skipped (--no-upgrade)" >> "$SUMMARY"; fi
if [ "$M12" = 1 ]; then run_part "M12" part_m12; else echo "M12: skipped (runs on linux-x64 and win-x64)" >> "$SUMMARY"; fi

echo
while IFS= read -r line; do echo "$NAME | $line"; done < "$SUMMARY"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### install test: $NAME"
    echo
    while IFS= read -r line; do echo "- $line"; done < "$SUMMARY"
  } >> "$GITHUB_STEP_SUMMARY"
fi
exit "$FAILED"
