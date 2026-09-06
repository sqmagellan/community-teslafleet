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
# Pinned to the same version .github/workflows/ci.yml uses. With `latest` on both
# sides the gate and CI silently drift onto different linters, and "it passed
# locally" stops being evidence. Bump the two together.
#
# The -alpine variant has no bash, so the wrapper below runs under sh.
LINT_IMAGE="${GATE_LINT_IMAGE:-golangci/golangci-lint:v2.13.2-alpine}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UPSTREAM_REF="${GATE_UPSTREAM_REF:-upstream-main}"

GATEWAY="${GATE_GATEWAY_CONTAINER:-teslafleet-gateway-1}"
TELEMETRY="${GATE_TELEMETRY_CONTAINER:-teslafleet-fleet-telemetry-1}"
FLEETAPI="${GATE_FLEETAPI_URL:-http://127.0.0.1:4460}"
HEALTH_DIR="${GATE_HEALTH_DIR:-/home/ames/home-assistant/config/ames-health}"
# /debug/state is gated (default off upstream, token-protected here), so the live
# checks have to authenticate. Same file the container mounts read-only.
DEBUG_TOKEN_FILE="${GATE_DEBUG_TOKEN_FILE:-/home/ames/.tesla-fleet-api/debug_token}"
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
# GOTOOLCHAIN is pinned to the go.mod directive on purpose. Without it the
# container uses whatever Go it ships, which is newer -- so govulncheck scanned a
# PATCHED standard library and reported clean while CI, which honours go.mod,
# found six reachable stdlib advisories. A gate that grades a different binary
# from the one that ships is not a gate.
GO_PIN="go$(awk '/^go /{print $2; exit}' "$REPO/go.mod")"

gorun() {
  docker run --rm -v "$REPO:/src:ro" \
    -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomod -e GOPATH=/tmp/gopath \
    -e GOTOOLCHAIN="$GO_PIN" \
    "$GO_IMAGE" bash -c "cp -a /src /work && cd /work && $1"
}

if [ "$DO_STATIC" = 1 ]; then
  stage "static analysis"

  # gofmt: delegate to .github/gofmt-drift.sh, which CI also runs. Two gates
  # with two notions of "pass" is how a clean local run turns red on push --
  # which is exactly what happened when CI held changed files to `gofmt -l`
  # while this script asked only that a change add no NEW drift.
  #
  # Run inside the Go image so it does not depend on a local gofmt.
  if docker run --rm -v "$REPO:/src:ro" -w /src "$GO_IMAGE" \
      bash -c "git config --global --add safe.directory /src && .github/gofmt-drift.sh $UPSTREAM_REF" \
      >/tmp/gate-fmt.log 2>&1; then
    ok "$(tail -1 /tmp/gate-fmt.log)"
  else
    bad "gofmt: this change added formatting drift"; tail -8 /tmp/gate-fmt.log
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
      "$LINT_IMAGE" sh -c "cp -a /src /work && cd /work && golangci-lint run ./..." \
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

  # /debug/state is off by default in the code. Here it must be ON, because the
  # health probe and the checks above read it. Whether a TOKEN is also enforced
  # depends on whether the token file is configured — assert whichever is true, so
  # this cannot silently regress in either direction.
  code_no_token=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$FLEETAPI/debug/state")
  code_token=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -H "X-Debug-Token: $DEBUG_TOKEN" "$FLEETAPI/debug/state")
  token_configured=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$GATEWAY" 2>/dev/null \
    | grep -cE '^TGW_DEBUG_TOKEN(_FILE)?=.' || true)
  if [ "${token_configured:-0}" -gt 0 ]; then
    if [ "$code_token" = "200" ] && [ "$code_no_token" = "403" ]; then
      ok "/debug/state is token-gated (403 without, 200 with)"
    elif [ "$code_no_token" = "200" ]; then
      bad "/debug/state served WITHOUT a token even though one is configured — file not loaded?"
    else
      bad "/debug/state gate unexpected: no-token=$code_no_token with-token=$code_token"
    fi
  elif [ "$code_no_token" = "200" ]; then
    skip "/debug/state enabled with NO token (loopback-bound only) — pending the credential-file permission decision"
  else
    bad "/debug/state is not reachable (http $code_no_token) but the probe needs it"
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

  # Secrets in the container environment are visible in `docker inspect` and to
  # every child process. The code supports the _FILE form for all of these; the
  # rollout is pending a decision about credential-file permissions (the container
  # runs as uid 65532 and cannot read a 0600 file owned by ames).
  #
  # A secret that has BOTH an inline value and a _FILE counterpart is a real
  # regression -- the file form is in use and the inline copy came back -- so that
  # fails. Inline-only is reported, not failed, so a known interim does not block
  # a deploy while still being impossible to forget.
  env_dump=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$GATEWAY" 2>/dev/null)
  inline=""; regressed=""
  for v in TGW_TESLA_CLIENT_SECRET TGW_TESLA_REFRESH_TOKEN TGW_ONBOARD_PASSWORD TGW_HA_PASSWORD TGW_DEBUG_TOKEN; do
    has_inline=$(printf '%s\n' "$env_dump" | grep -cE "^$v=." || true)
    has_file=$(printf '%s\n' "$env_dump" | grep -cE "^${v}_FILE=." || true)
    if [ "$has_inline" -gt 0 ] && [ "$has_file" -gt 0 ]; then
      regressed="$regressed $v"
    elif [ "$has_inline" -gt 0 ]; then
      inline="$inline $v"
    fi
  done
  if [ -n "$regressed" ]; then
    bad "secret set BOTH inline and by file (inline copy should be deleted):$regressed"
  elif [ -n "$inline" ]; then
    skip "secrets still inline in the environment:$inline (pending the credential-file permission decision)"
  else
    ok "no secrets in the container environment (files only)"
  fi

  # ---- Phase 4: vehicle_data gaps, and HA availability ---------------------
  # is_climate_on was absent from the emulated document entirely, and the two SOC
  # figures were the same telemetry field published twice.
  for id in $(printf '%s' "$vehicles" | jq -r '.response[].id' 2>/dev/null); do
    vd=$(curl -s --max-time 10 "$FLEETAPI/api/1/vehicles/$id/vehicle_data")
    vin=$(printf '%s' "$vd" | jq -r '.response.vin // ""')
    # has(), not `//`: is_climate_on is a BOOL and jq's // treats false as empty,
    # so a present-and-false field would read as absent. (That exact mistake
    # produced a wrong measurement of ChargePortDoorOpen in phase 2.)
    if [ "$(printf '%s' "$vd" | jq -r '.response.climate_state | has("is_climate_on")')" = "true" ]; then
      ok "is_climate_on present for ...${vin: -6} ($(printf '%s' "$vd" | jq -r '.response.climate_state.is_climate_on'))"
    else
      bad "is_climate_on absent from climate_state for ...${vin: -6}"
    fi
    lvl=$(printf '%s' "$vd" | jq -r '.response.charge_state.battery_level // -1')
    usable=$(printf '%s' "$vd" | jq -r '.response.charge_state.usable_battery_level // -1')
    if [ "$lvl" -lt 0 ] || [ "$usable" -lt 0 ]; then
      skip "SOC pair for ...${vin: -6} (no SOC telemetry yet)"
    elif [ "$usable" -le "$lvl" ]; then
      ok "usable_battery_level $usable <= battery_level $lvl for ...${vin: -6}"
    else
      bad "usable_battery_level $usable > battery_level $lvl for ...${vin: -6}"
    fi
  done

  # Retained state + an availability topic. Checked at the BROKER, because the
  # retain flag is a property of what the broker stored, not of anything visible
  # in our own config: subscribing normally would see the 2s republish either way.
  MOSQ=${GATE_MOSQUITTO:-teslamate-mosquitto-1}
  ha_base=$(printf '%s\n' "$env_dump" | sed -n 's/^TGW_HA_STATE_TOPIC_BASE=//p')
  ha_base=${ha_base:-tgw}
  if ! docker ps --format '{{.Names}}' | grep -qx "$MOSQ"; then
    skip "MQTT checks (broker container $MOSQ not running)"
  else
    retained=$(docker exec "$MOSQ" mosquitto_sub -h localhost -v --retained-only -W 3 \
      -t "$ha_base/#" -t "homeassistant/#" 2>/dev/null)
    avail=$(printf '%s\n' "$retained" | sed -n "s|^$ha_base/availability ||p" | tail -1)
    if [ "$avail" = "online" ]; then
      ok "availability topic $ha_base/availability retained as online"
    else
      bad "availability topic $ha_base/availability = '${avail:-absent}', want online"
    fi
    st_count=$(printf '%s\n' "$retained" | grep -cE "^$ha_base/[^/ ]+/state " || true)
    if [ "$st_count" -gt 0 ]; then
      ok "state published retained ($st_count vehicle topic(s) held by the broker)"
    else
      bad "no retained state topic under $ha_base/ — HA starts blank after a restart"
    fi
    # Every discovery config WE published must name the availability topic; one
    # that does not keeps showing its last value after the gateway dies.
    no_avail=$(printf '%s\n' "$retained" | grep '^homeassistant/' | grep 'community-teslafleet' \
      | grep -vc 'availability_topic' || true)
    our_cfgs=$(printf '%s\n' "$retained" | grep -c 'community-teslafleet' || true)
    if [ "$our_cfgs" -eq 0 ]; then
      bad "no discovery configs of ours are retained under homeassistant/"
    elif [ "$no_avail" -eq 0 ]; then
      ok "all $our_cfgs retained discovery configs carry availability_topic"
    else
      bad "$no_avail of $our_cfgs retained discovery configs lack availability_topic"
    fi
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
