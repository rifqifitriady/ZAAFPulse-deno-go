#!/bin/bash
# Deno spike for ZAAFPulse (P1 of docs/migration/2026-11-10-deno-go-migration-plan.md). DENO=path/to/deno, OUT=output dir.
set -u
D=${DENO:-deno}; S=$(cd "$(dirname "$0")" && pwd); A=$(cd "$S/../.." && pwd); O=${OUT:-$(mktemp -d)}
echo "== deno"; $D --version | head -1
echo "== 1. compat probe"; $D run -A "$S/deno-probe.js"
echo "== 2. test suite under deno (node:test)"; cd "$A" && ( time $D test -A --no-check --quiet test/ ) > "$O/tests.log" 2>&1; tail -15 "$O/tests.log"
echo "== 3. boot server under a narrow permission set (demo mode, temp data)"
T=$(mktemp -d); ( DEMO=1 ZAAFPULSE_PORT=10460 ZAAFPULSE_DATA=$T ZAAFPULSE_NOTIFY=off ZAAFPULSE_RUNTIME_WATCH=off \
  $D run --allow-read="$A,$T,$HOME" --allow-write="$T" --allow-net=127.0.0.1,localhost --allow-env --allow-run=which,security,ps \
  --allow-sys "$A/server.js" > "$O/server.log" 2>&1 & echo $! > "$O/server.pid" )
for i in $(seq 1 40); do curl -s -o /dev/null http://127.0.0.1:10460/login.html && break; sleep 0.5; done
echo "login.html: $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:10460/login.html)"; kill "$(cat "$O/server.pid")" 2>/dev/null; tail -8 "$O/server.log"
echo "== 4. compile to all 6 targets (output in $O)"; cd "$O"
for t in aarch64-apple-darwin x86_64-apple-darwin aarch64-unknown-linux-gnu x86_64-unknown-linux-gnu x86_64-pc-windows-msvc aarch64-pc-windows-msvc; do
  $D compile -A --quiet --no-check --target $t --include "$A/lib" --include "$A/public" -o "out/zp-$t" "$A/server.js" >/dev/null 2>"err-$t.log" \
    && f=$(ls out/zp-$t* | head -1) && printf "%-26s %6.1f MB  %s\n" $t "$(echo "$(stat -f%z "$f")/1048576" | bc -l)" "$(file -b "$f" | cut -c1-50)" \
    || { echo "$t FAILED"; head -3 "err-$t.log"; }
done
