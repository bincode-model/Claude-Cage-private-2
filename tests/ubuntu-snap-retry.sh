#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source internal/cage/ubuntu-browser.sh
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
export scratch
cat >"$scratch/sudo" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$scratch/args"
n=$(($(wc -l <"$scratch/args")))
case "$scenario" in
  transient) if (( n < 3 )); then echo 'connection reset by peer' >&2; exit 19; fi ;;
  permanent) echo 'signature verification failed' >&2; exit 23 ;;
  exhausted) echo 'net/http: timeout awaiting response headers' >&2; exit 42 ;;
esac
STUB
cat >"$scratch/sleep" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$1" >>"$scratch/sleeps"
STUB
chmod +x "$scratch/sudo" "$scratch/sleep"
export PATH="$scratch:$PATH"
for scenario in transient permanent exhausted; do
  export scenario
  : >"$scratch/args"
  : >"$scratch/sleeps"
  status=0
  cage_snap_store_retry install chromium --channel=latest/stable 2>"$scratch/log" || status=$?
  case "$scenario" in
    transient) expected=0; count=3; delays=2 ;;
    permanent) expected=23; count=1; delays=0 ;;
    exhausted) expected=42; count=3; delays=2 ;;
  esac
  [[ $status == "$expected" && $(wc -l <"$scratch/args") == "$count" && $(wc -l <"$scratch/sleeps") == "$delays" ]]
  [[ $(sort -u "$scratch/args") == 'snap install chromium --channel=latest/stable' ]]
done
printf 'Snap Store retry checks passed\n'
