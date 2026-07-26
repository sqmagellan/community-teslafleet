#!/usr/bin/env bash
# gate.sh — everything that must pass before this fork is deployed.
#
# Why a script and not just "go test": the failures this project actually had in
# production were NOT test failures. They were a wedged ZMQ socket that kept
# publishing stale data, a hard-coded healthz that stayed green through it, and
# a container that built fine while serving the wrong image. So the gate has two
# halves:
#
#   static  (default)  fmt / vet / test -race / govulncheck / docker build
#   live    (--live)   prove the DEPLOYED thing actually ingests and serves
#
# The live half is the one that catches the bug class that bit us. Run it after
# every deploy, not just when something looks wrong.
#
# Usage:
#   hack/gate.sh                  # static only
#   hack/gate.sh --live           # static + live checks against the running stack
#   hack/gate.sh --live --zmq-restart
#                                 # also restart fleet-telemetry to prove the
#                                 # gateway re-dials (BRIEFLY interrupts ingest)
#   hack/gate.sh --live-only      # skip static, just verify a running deploy
#
# No `set -e`: every stage reports independently and the summary is the verdict.
set -uo pipefail

GO_IMAGE="${GATE_GO_IMAGE:-golang:1.25}"
LINT_IMAGE="${GATE_LINT_IMAGE:-golangci/golangci-lint:latest}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UPSTREAM_REF="${GATE_UPSTREAM_REF:-upstream-main}"

GATEWAY="${GATE_GATEWAY_CONTAINER:-teslafleet-gateway-1}"
TELEMETRY="${GATE_TELEMETRY_CONTAINER:-teslafleet-fleet-telemetry-1}"
FLEETAPI="${GATE_FLEETAPI_URL:-http://127.0.0.1:4460}"
HEALTH_DIR="${GATE_HEALTH_DIR:-/home/ames/home-assistant/config/ames-health}"
# /debug/state is gated (default off upstream, token-protected here), so the live
# checks have to authenticate. Same file the container mounts read-only.
DEBUG_TOKEN_FILE="${GATE_DEBUG_TOKEN_FILE:-/home/ames/.teslafleet-debug-token}"
DEBUG_TOKEN="$(cat "$DEBUG_TOKEN_FILE" 2>/dev/null || true)"

DO_STATIC=1
DO_LIVE=0
DO_ZMQ_RESTART=0
for arg in "$@"; do
  case "$arg" in
    --live)         DO_LIVE=1 ;;
    --live-only)    DO_LIVE=1; DO_STATIC=0 ;;
    --zmq-restart)  DO_ZMQ_RESTART=1 ;;
    -h|--help)      sed -n '2,28p' "$0"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

PASS=(); FAIL=(); SKIP=()
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS+=("$1"); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL+=("$1"); }
skip() { printf '  \033[33mSKIP\033[0m  %s\n' "$1"; SKIP+=("$1"); }
stage() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# gorun executes a go toolchain command against a THROWAWAY COPY of the repo.
# The copy matters: it keeps the container from writing root-owned build
# artifacts or a mutated go.mod back into the working tree.
gorun() {
  docker run --rm -v "$REPO:/src:ro" \
    -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomod -e GOPATH=/tmp/gopath \
    "$GO_IMAGE" bash -c "cp -a /src /work && cd /work && $1"
}

if [ "$DO_STATIC" = 1 ]; then
  stage "static analysis"

  # Hold OUR changes to gofmt, but do not fail on drift we inherited.
  #
  # The upstream tree carries pre-existing formatting deviations (struct field
  # alignment). Reformatting it wholesale would conflict with every future
  # upstream merge, so the rule is not "changed files must be clean" -- that
  # punishes you for touching an already-dirty file -- but "your change must not
  # ADD deviation". Compare each changed file's normalized gofmt diff against the
  # same file's diff at $UPSTREAM_REF; equal means you introduced none.
  changed=$(cd "$REPO" && git diff --name-only "$UPSTREAM_REF" -- '*.go' 2>/dev/null)
  if [ -z "$changed" ]; then
    skip "gofmt (no .go changes vs $UPSTREAM_REF)"
  else
    WORK=$(mktemp -d)
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      mkdir -p "$WORK/new/$(dirname "$f")" "$WORK/old/$(dirname "$f")"
      [ -f "$REPO/$f" ] && cp "$REPO/$f" "$WORK/new/$f"
      (cd "$REPO" && git show "$UPSTREAM_REF:$f" 2>/dev/null) > "$WORK/old/$f"
      # A file that does not exist upstream has no inherited drift to compare.
      [ -s "$WORK/old/$f" ] || rm -f "$WORK/old/$f"
    done <<< "$changed"

    cat > "$WORK/fmtcheck.py" <<'PYEOF'
import collections, re, sys

drift = collections.defaultdict(list)
side = None
for line in sys.stdin.read().splitlines():
    m = re.match(r"^diff (?:-u )?/w/(old|new)/(.+)\.orig\b", line)
    if m:
        side = (m.group(1), m.group(2))
        continue
    if side and re.match(r"^[+-]", line) and not re.match(r"^(\+\+\+|---)", line):
        # Normalize away whitespace: we care WHICH lines gofmt rewrites, not how
        # far they moved, and line numbers shift as code is added above them.
        drift[side].append("".join(line.split()))

for f in sorted({name for _, name in drift}):
    if sorted(drift.get(("new", f), [])) != sorted(drift.get(("old", f), [])):
        print(f)
PYEOF

    raw=$(docker run --rm -v "$WORK:/w:ro" "$GO_IMAGE" gofmt -d /w/old /w/new 2>/dev/null)
    newdrift=$(printf '%s\n' "$raw" | python3 "$WORK/fmtcheck.py")
    if [ -z "$newdrift" ]; then
      inherited=$(printf '%s\n' "$raw" | grep -cE '^diff ' || true)
      ok "gofmt: no new drift in changed files ($(printf '%s\n' "$changed" | grep -c . ) changed, $inherited pre-existing deviation(s) tolerated)"
    else
      bad "gofmt: your change ADDED formatting drift in: $(printf '%s' "$newdrift" | tr '\n' ' ')"
      printf '        run: gofmt -w %s\n' "$(printf '%s' "$newdrift" | tr '\n' ' ')"
    fi
    rm -rf "$WORK"
  fi

  if gorun "go vet ./..." >/tmp/gate-vet.log 2>&1; then
    ok "go vet"
  else
    bad "go vet (see /tmp/gate-vet.log)"; tail -20 /tmp/gate-vet.log
  fi

  if gorun "go test ./... -race -count=1" >/tmp/gate-test.log 2>&1; then
    ok "go test -race ($(grep -c '^ok' /tmp/gate-test.log) packages)"
  else
    bad "go test -race (see /tmp/gate-test.log)"; grep -E '^(---|FAIL|\s+.*_test)' /tmp/gate-test.log | head -25
  fi

  if gorun "go install golang.org/x/vuln/cmd/govulncheck@latest >/dev/null 2>&1 && /tmp/gopath/bin/govulncheck ./..." \
      >/tmp/gate-vuln.log 2>&1; then
    ok "govulncheck (no reachable vulnerabilities)"
  else
    bad "govulncheck found reachable vulnerabilities"
    grep -E '^Vulnerability|Fixed in|Your code is affected' /tmp/gate-vuln.log | head -20
  fi

  # Blocking, and run from a container so it does not depend on a local install
  # (CI runs the same config). errcheck is on: see .golangci.yml for why the only
  # exemptions are unactionable Close calls.
  if docker run --rm -v "$REPO:/src:ro" \
      -e GOCACHE=/tmp/gc -e GOMODCACHE=/tmp/gm -e GOLANGCI_LINT_CACHE=/tmp/glc \
      "$LINT_IMAGE" bash -c "cp -a /src /work && cd /work && golangci-lint run ./..." \
      >/tmp/gate-lint.log 2>&1; then
    ok "golangci-lint (errcheck enabled)"
  else
    bad "golangci-lint (see /tmp/gate-lint.log)"; tail -20 /tmp/gate-lint.log
  fi

  stage "container build"
  if docker build -q -t community-teslafleet:gate-test "$REPO" >/tmp/gate-build.log 2>&1; then
    ok "docker build"
  else
    bad "docker build (see /tmp/gate-build.log)"; tail -20 /tmp/gate-build.log
  fi
fi

if [ "$DO_LIVE" = 1 ]; then
  stage "live: is the deployed gateway actually working?"

  if [ "$(docker inspect -f '{{.State.Running}}' "$GATEWAY" 2>/dev/null)" = "true" ]; then
    ok "$GATEWAY running"
  else
    bad "$GATEWAY not running"
  fi

  # The emulated Fleet API is what TeslaMate talks to. If this is wrong,
  # TeslaMate silently stops recording.
  vehicles=$(curl -s --max-time 10 "$FLEETAPI/api/1/vehicles")
  n=$(printf '%s' "$vehicles" | jq -r '.response | length' 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 1 ]; then
    ok "emulated Fleet API lists $n vehicle(s)"
  else
    bad "emulated Fleet API returned no vehicles"
  fi

  # car_type must be derived, never the old hardcoded model3 default.
  for id in $(printf '%s' "$vehicles" | jq -r '.response[].id' 2>/dev/null); do
    vd=$(curl -s --max-time 10 "$FLEETAPI/api/1/vehicles/$id/vehicle_data")
    ct=$(printf '%s' "$vd" | jq -r '.response.vehicle_config.car_type // "absent"')
    vin=$(printf '%s' "$vd" | jq -r '.response.vin // ""')
    # Compare against the model line encoded in position 4 of the VIN itself.
    case "${vin:3:1}" in
      S) want=models ;; 3) want=model3 ;; X) want=modelx ;;
      Y) want=modely ;; C) want=cybertruck ;; R) want=roadster ;;
      *) want="" ;;
    esac
    if [ -z "$want" ]; then
      skip "car_type for ...${vin: -6} (unknown model line ${vin:3:1})"
    elif [ "$ct" = "$want" ]; then
      ok "car_type=$ct matches VIN position 4 for ...${vin: -6}"
    else
      bad "car_type=$ct but VIN position 4 says $want for ...${vin: -6}"
    fi
  done

  # /healthz is now a real readiness verdict, not a hard-coded "ok". A 503 here
  # means either the ZMQ link is down or an online car has gone silent past
  # stale_after_seconds — both of which used to be invisible from outside.
  hz_code=$(curl -s -o /tmp/gate-healthz.json -w '%{http_code}' --max-time 10 "$FLEETAPI/healthz")
  hz_status=$(jq -r '.status // "missing"' /tmp/gate-healthz.json 2>/dev/null)
  hz_conn=$(jq -r '.ingest_connected' /tmp/gate-healthz.json 2>/dev/null)
  hz_age=$(jq -r '.last_ingest_age_s' /tmp/gate-healthz.json 2>/dev/null)
  if [ "$hz_code" = "200" ] && [ "$hz_status" = "ok" ]; then
    ok "/healthz ready (ingest_connected=$hz_conn last_ingest_age=${hz_age}s)"
  elif [ "$hz_status" = "missing" ]; then
    bad "/healthz did not return the readiness document (http $hz_code) — old build deployed?"
  else
    bad "/healthz $hz_code $hz_status: $(jq -r '.reason // "no reason given"' /tmp/gate-healthz.json)"
  fi

  # Per-VIN last-ingest timestamps must be present, because the alternative
  # (diffing successive /debug/state responses) silently cannot work: every field
  # carries an age_s that ticks, so a whole-object diff always looks changed.
  ts_missing=$(curl -s --max-time 10 -H "X-Debug-Token: $DEBUG_TOKEN" "$FLEETAPI/debug/state" \
    | jq -r '[to_entries[] | select(.value.last_ingest_unix == null) | .key] | length' 2>/dev/null)
  if [ "${ts_missing:-1}" = "0" ]; then
    ok "/debug/state exposes last_ingest_unix for every vehicle"
  else
    bad "$ts_missing vehicle(s) in /debug/state have no last_ingest_unix"
  fi

  # Our own ingest probe is the only thing that detects a SILENT stall.
  if [ -r "$HEALTH_DIR/teslafleet-ingest.txt" ]; then
    st=$(tr -d '\n' < "$HEALTH_DIR/teslafleet-ingest.txt")
    detail=$(tr -d '\n' < "$HEALTH_DIR/teslafleet-ingest-detail.txt" 2>/dev/null)
    case "$st" in
      ok)            ok "ingest probe: ok ($detail)" ;;
      reconnecting)  skip "ingest probe: reconnecting ($detail) — self-heals, recheck" ;;
      *)             bad "ingest probe: $st ($detail)" ;;
    esac
  else
    skip "ingest probe file not readable at $HEALTH_DIR"
  fi

  # HA warning spam regression (D2): an enum sensor rendering '' logs per message.
  spam=$(docker logs --since 10m homeassistant 2>&1 | grep -c "Ignoring invalid option" || true)
  if [ "${spam:-0}" -eq 0 ]; then
    ok "no 'Ignoring invalid option' warnings in the last 10m"
  else
    bad "$spam 'Ignoring invalid option' warnings in 10m (enum default regression)"
  fi

  if [ "$DO_ZMQ_RESTART" = 1 ]; then
    stage "live: ZMQ resilience regression test"
    # THE bug that wedged us silently: on the first recv error the old build
    # blocked forever instead of re-dialing. Prove the fix is still in.
    before=$(docker logs --since 60m "$GATEWAY" 2>&1 | grep -c "zmq ingest reconnected" || true)
    docker restart "$TELEMETRY" >/dev/null 2>&1
    sleep 12
    after=$(docker logs --since 60m "$GATEWAY" 2>&1 | grep -c "zmq ingest reconnected" || true)
    if [ "${after:-0}" -gt "${before:-0}" ]; then
      ok "gateway re-dialed ZMQ after telemetry restart ($before -> $after)"
    else
      bad "gateway did NOT re-dial after telemetry restart — the wedge is back"
    fi
  else
    skip "ZMQ resilience test (pass --zmq-restart; briefly interrupts ingest)"
  fi

  # The gate is off by default upstream; here it must be ON (the probe and these
  # checks depend on it) and it must REJECT a request with no token. A 200
  # without a token means the token was silently not loaded.
  code_no_token=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$FLEETAPI/debug/state")
  code_token=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -H "X-Debug-Token: $DEBUG_TOKEN" "$FLEETAPI/debug/state")
  if [ "$code_token" = "200" ] && [ "$code_no_token" = "403" ]; then
    ok "/debug/state is token-gated (403 without, 200 with)"
  elif [ "$code_token" = "200" ] && [ "$code_no_token" = "200" ]; then
    bad "/debug/state served WITHOUT a token — TGW_DEBUG_TOKEN_FILE not loaded?"
  else
    bad "/debug/state gate unexpected: no-token=$code_no_token with-token=$code_token"
  fi

  # The onboarding wizard should not be listening at all (S4).
  if curl -s -o /dev/null --max-time 3 http://127.0.0.1:8099/ 2>/dev/null; then
    bad "onboarding wizard is still answering on 127.0.0.1:8099"
  else
    ok "onboarding wizard is not listening on 8099"
  fi

  # Hardening flags must actually be applied to the RUNNING container, not just
  # present in the compose file.
  ro=$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$GATEWAY" 2>/dev/null)
  caps=$(docker inspect -f '{{.HostConfig.CapDrop}}' "$GATEWAY" 2>/dev/null)
  nnp=$(docker inspect -f '{{.HostConfig.SecurityOpt}}' "$GATEWAY" 2>/dev/null)
  if [ "$ro" = "true" ] && [ "$caps" = "[ALL]" ] && [ "${nnp#*no-new-privileges}" != "$nnp" ]; then
    ok "container hardening applied (read_only, cap_drop ALL, no-new-privileges)"
  else
    bad "container hardening NOT applied: read_only=$ro cap_drop=$caps security_opt=$nnp"
  fi

  # A secret in the environment is the thing S1 removed; catch it coming back.
  leaked=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$GATEWAY" 2>/dev/null \
    | grep -E '^TGW_(TESLA_CLIENT_SECRET|TESLA_REFRESH_TOKEN|ONBOARD_PASSWORD|HA_PASSWORD|DEBUG_TOKEN)=.' \
    | cut -d= -f1 | tr '\n' ' ')
  if [ -z "$leaked" ]; then
    ok "no secrets in the container environment (files only)"
  else
    bad "secret(s) back in the container environment: $leaked"
  fi

  if [ -x /home/ames/homelab-maint/custom-guard.py ]; then
    if /home/ames/homelab-maint/custom-guard.py check >/tmp/gate-guard.log 2>&1; then
      ok "custom-guard: all customizations present"
    else
      bad "custom-guard reported a problem (see /tmp/gate-guard.log)"; tail -15 /tmp/gate-guard.log
    fi
  else
    skip "custom-guard.py not found"
  fi
fi

stage "summary"
printf '  %d passed, %d failed, %d skipped\n' "${#PASS[@]}" "${#FAIL[@]}" "${#SKIP[@]}"
if [ "${#FAIL[@]}" -gt 0 ]; then
  printf '\n\033[31mGATE FAILED\033[0m — do not deploy:\n'
  for f in "${FAIL[@]}"; do printf '  - %s\n' "$f"; done
  exit 1
fi
printf '\n\033[32mGATE PASSED\033[0m\n'
