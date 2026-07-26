# FORK.md — what this fork changes, and why it exists

This is a fork of **[LasseLegarth/community-teslafleet](https://github.com/LasseLegarth/community-teslafleet)**
(MIT). All of the original design and the overwhelming majority of the code is
his work; the upstream `LICENSE` and commit authorship are preserved unchanged.
If you are looking for the project itself, start upstream.

## Why fork

Upstream solves a genuinely useful problem: it turns Tesla's **Fleet Telemetry**
push stream into (a) Home Assistant MQTT discovery entities and (b) an emulated
Fleet API that TeslaMate can poll, so TeslaMate keeps working without paying for
per-request Fleet API calls.

We run it in production against two vehicles. Two things pushed us from
"clone and `git pull`" to "fork":

1. **The published container image is frozen.** The `release.yml` workflow only
   fires on `v*` tags, and the last tag predates several important fixes — most
   critically the one where a dropped ZMQ peer left ingest wedged **forever**
   while MQTT kept happily republishing stale values. Running `:latest` from the
   registry means running without that fix. We therefore build from source, and
   once you build from source you own the build.
2. **We carry local patches.** Every patch in a dirty working tree is a merge
   hazard. Patches belong on branches with commit messages that explain them.

Upstream `main` is also unverified by CI (see the `ci` workflow here), and the
sole maintainer has not yet responded to the one issue filed against the repo.
That is not a criticism — it is a bus-factor fact we have to plan around.

## Branch layout

| Branch | Meaning |
|---|---|
| `upstream-main` | Pure mirror of `upstream/main`. Never edited directly. |
| `ames-main` | The branch we deploy. `upstream-main` plus the merges below. |
| `fix/<topic>` | One self-contained change, cut from `upstream-main`, so it can be sent upstream as a PR containing nothing else. |

Current `fix/` branches, in the order they should go upstream:

| Branch | Change |
|---|---|
| `fix/enum-none` | absent enum fields render as `none`, not `''` |
| `fix/vin-cartype` | derive `car_type` from the VIN |
| `fix/plugged-in` | corroborate a stale `ChargePortLatch` |
| `fix/readiness` | real `/healthz` + last-ingest timestamps |
| `fix/error-handling` | stop discarding errors that hide real failures |
| `fix/secret-files` | read any string setting from `<VAR>_FILE` |
| `fix/token-cache-validation` | a cached refresh token satisfies validation |
| `fix/log-redaction` | keep credentials out of the request log |
| `fix/debug-gate` | `/debug/state` off by default, token-gated when on |
| `fix/vehicle-data-gaps` | publish `is_climate_on`; stop reporting one SOC as two |
| `feat/ha-availability` | retained state + an availability (LWT) topic |
| `fix/shutdown-ordering` | wait for the shutdown flush instead of racing exit |
| `feat/vehicle-data-seed` | gated fetch of the real `vehicle_data` (needs `fix/debug-gate`) |
| `fix/rediscover-on-reconnect` | generic discovery comes back with the curated kind |
| `fix/dep-bump` | close three reachable advisories |
| `fix/ci` | CI on push/PR + blocking linter (depends on `fix/error-handling`) |

Taking upstream work:

```bash
git fetch upstream
git checkout upstream-main && git merge --ff-only upstream/main
git checkout ames-main && git merge upstream-main
hack/gate.sh --live        # never deploy an unverified merge
```

## Changes in this fork

Each is a `fix/` branch merged into `ames-main`, and each is intended to go
upstream as its own PR.

### `fix/enum-none` — absent enum fields must render as `none`, not `''`

Home Assistant validates a sensor with `device_class: enum` against its declared
`options` list. The empty string is not a member, so an absent telemetry key made
HA log `Ignoring invalid option ... got ''` **on every published message** —
roughly 86,000 warning lines per day with two parked cars, and the affected
entities never became usable. Rendering the Jinja literal `none` yields state
`unknown`, which HA accepts for any entity regardless of `options`.

Non-enum entities keep `default('')`, so payloads for entities that already work
are byte-identical.

### `fix/vin-cartype` — derive `car_type` from the VIN

The default `vehicle_data` template hardcoded `car_type: model3` (and
`trim_badging: 74d`). If your car is not a Model 3, the emulated Fleet API
asserted the wrong model, and that propagates into TeslaMate and anything else
reading `vehicle_data`. The only workaround was to hand-write a captured
template per VIN and keep it correct forever — which is exactly the kind of
manual state that silently drifts.

A Tesla VIN already states the model. The new `internal/vin` package decodes:

- **position 4** → model line → `car_type` (`models`, `model3`, `modelx`,
  `modely`, `cybertruck`, `roadster`)
- **position 9** → ISO 3779 check digit, so a typo is detectable
- **position 10** → model year
- **position 11** → assembly plant

Precedence for `car_type`: an explicit `car_type:` in the vehicle config wins,
then the VIN, then whatever a captured template supplied. The default skeleton no
longer asserts a model at all, so it can never mislabel a vehicle.

Deliberately **not** inferred: trim, paint, option packages. Those are not
encoded in a Tesla VIN in any stable documented way. A confidently wrong
`car_type` is worse than an absent one, because a caller cannot tell it is wrong
— which is precisely the failure the hardcoded `model3` caused. For the same
reason the check digit is verified before anything derived from a VIN is trusted,
and Tesla Semi is left unmapped rather than guessed.

### `fix/plugged-in` — a stale `ChargePortLatch` must be corroborated

`plugged_in` was `strings.Contains(ChargePortLatch, "Engaged")` and nothing else.
The latch is the right primary signal — it is discrete and streams both ways —
but it can go stale Engaged and then never re-streams. Observed on a car that had
been unplugged for hours: `ChargePortLatchEngaged` alongside
`DetailedChargeStateDisconnected` and `ChargePortDoorOpen=false`, i.e. a shut
charge port with no cable in it, publishing `plugged_in: true` indefinitely while
TeslaMate — reading charge state and the port door — correctly said false.

The latch stays primary and is only ever *vetoed*, never overridden, and only
when two independent discrete signals both say there is no cable. An absent field
vetoes nothing, so the fallback is the previous "trust the latch" rather than a
guess. The veto can only turn `plugged_in` off, so the old sticky-`ChargingCableType`
failure mode cannot return.

### `fix/readiness` — real `/healthz`, plus the timestamps monitoring needs

`/healthz` returned a hard-coded `ok`. That is worse than no probe: the failure
this project actually has is a silent stall — the publisher republishes the
in-memory store off a ticker, so a faulted ZMQ socket looks exactly like a parked
fleet — and a hard-coded `ok` guarantees anything built on it misses that.

It now serves `ingest link up AND (no vehicle online OR data newer than
stale_after_seconds)`, 200 or 503 with a reason. The freshness half has to be
conditional on a car being online, or the check fails every night on a sleeping
fleet; a quiet fleet reports ready so a 3am restart does not stay unready until
morning. `ingest_connected` is `null`, not `false`, when no ingest source is
attached — unknown is a third answer.

Also adds `store.LastIngest()`, `ingest.Consumer.Connected()`, and
`last_ingest_unix` / `last_ingest_age_s` / `field_count` per vehicle in
`/debug/state`. That last one closes a trap: without an explicit timestamp the
only external way to ask "is this vehicle still streaming?" is to diff successive
`/debug/state` responses, and that silently cannot work, because every field
carries an `age_s` that ticks on its own. Measured on a parked car: two polls 3s
apart differ in 36 of 36 fields with zero value changes. A monitor built that way
can never detect a stall — ours was, and could not.

`internal/fleetapi` had no tests before this.

### `fix/error-handling` — stop discarding errors that hide real failures

Not a blanket sweep; each of these had no other symptom. `fleetapi` dropped every
JSON encode error (a truncated body to TeslaMate, reported nowhere). `ingest`
shared one bare `return` between a malformed connectivity payload — which breaks
online/asleep/offline detection wholesale — and the benign VIN-less case. The
three `os.Setenv` calls in the HA add-on's MQTT autodetection *are* that
function's output, so a failure meant starting with no broker having configured
nothing wrong. `onboard` left both public-key writes unchecked, and Tesla fetches
that key to verify domain ownership.

`recorder` was the worst: flush and close errors were discarded in `rotate()`,
`flushLoop()` and `Close()`, so a full disk lost telemetry silently. Two bugs fell
out of fixing it — `flushLoop` ran forever after `Close`, flushing a closed file
every 2s and throwing away the error, and `bufio.Writer` keeps its first error
sticky, so naive logging would emit a line per telemetry field and bury the first
failure. It now logs transitions: one line when recording breaks, one when it
recovers. `internal/recorder` had no tests before this either.

`defer resp.Body.Close()` and friends are deliberately left alone.

### `fix/secret-files` — read any string setting from `<VAR>_FILE`

Secrets could only be environment variables, which means they live in the compose
file, in `docker inspect` output, in the environment of every child process, and
in any crash dump that captures the environment. Tesla's own vehicle-command proxy
already takes its client id and secret as *files*, so a deployment ends up with two
conventions for the same credential.

`setStr` is the single choke point for every string setting, so the support goes
there and covers `TGW_TESLA_CLIENT_SECRET`, `TGW_TESLA_REFRESH_TOKEN`,
`TGW_HA_PASSWORD` and `TGW_ONBOARD_PASSWORD` without enumerating them.

`_FILE` wins when both are set — migrating a secret out of an env var must not
silently keep reading the stale inline copy you are deleting. An unreadable path
logs an error and falls back rather than leaving the value empty, because a typo'd
path that reads as "never configured" surfaces as a confusing auth failure
somewhere unrelated.

### `fix/token-cache-validation` — a cached refresh token is a valid credential

`commands.enabled` required `commands.refresh_token` to be non-empty. But that
value is only ever a **seed**: Tesla rotates the token on every refresh, so within
seconds of first start the config copy is stale and `token_cache` holds the live
one — the relay logs `loaded refresh token from cache` and never reads the config
value again.

Requiring it anyway forces a stale credential to be kept forever in whatever holds
the config, where it is the copy that leaks and — because deleting it crash-loops
the gateway on a validation error — the copy nobody dares delete. Validation now
accepts either the seed or a non-empty cache file.

### `fix/log-redaction` — keep credentials out of the request log

The request logger formatted `r.URL.RawQuery` straight into a debug line.
TeslaMate's legacy streaming client passes its access token as a query parameter
and Tesla's OAuth flow puts an authorization code in one, so enabling debug
logging wrote live credentials into a log that outlives them and gets pasted into
bug reports. `redactQuery` keeps which parameters were sent — the useful part —
and replaces the values. A query that will not parse is dropped rather than logged
raw: if it cannot be parsed it cannot be redacted.

### `fix/debug-gate` — `/debug/state` off by default, token-gated when on

`/debug/state` is the most sensitive thing this process serves: every stored
telemetry field for every vehicle, which includes precise GPS, the active route's
destination and the odometer — a live location feed for a named car, previously
unauthenticated with nothing in front of it but whatever address the operator
bound to.

Now opt-in (`debug.state_enabled`, default false) with an optional shared secret
(`debug.token`). Disabled answers 404 rather than 403, because a 403 confirms the
endpoint exists. The token comes from `X-Debug-Token` or `Authorization: Bearer`
and deliberately **not** from a query parameter, which would be written into this
server's own request log. The comparison is constant time.

### `fix/vehicle-data-gaps` — `is_climate_on`, and one SOC reported as two

Two holes in the emulated `vehicle_data`, both filled from telemetry the gateway
already receives — no extra stream config, no API call.

`climate_state.is_climate_on` was **absent from the document entirely**: the
captured template ships an empty `climate_state`, the mapper overlaid only the two
temperatures, and so every consumer saw "no climate data" while `HvacPower`
streamed the whole time. It is now overlaid, and *omitted* rather than defaulted to
`false` when the enum cannot be classified — `HvacPowerStateUnknown` means the car
does not know either, and a confident `false` is worse than a missing key.

`battery_level` and `usable_battery_level` were both `Soc`. On a real car the
usable figure sits below the displayed one when the pack is cold, and TeslaMate
uses that gap to show the blue snowflake (teslamate#321) — publishing one value as
both made the gap structurally always zero.

Settled against real Fleet API documents fetched for two cars while they were
awake (see `feat/vehicle-data-seed`):

| telemetry `Soc` | telemetry `BatteryLevel` | API `battery_level` | API `usable_battery_level` |
|---|---|---|---|
| 59.221 | 59.553 | 60 | 59 |
| 69.541 | 69.851 | 70 | 70 |

So `BatteryLevel` is the displayed SOC, `Soc` is the usable one, and **the API
rounds where the obvious `int()` conversion truncates**. Truncating published 59/59
where the car itself reports 60/59: `battery_level` read one percent low most of
the time *and* the usable gap vanished. Rounding reproduces all four measured
values exactly. `min()` stays as the invariant that usable can never exceed
displayed.

`store.HvacOn` becomes the single place that decides what an `HvacPower` value
means, and `hadiscovery.hvacOn` delegates to it, so the HA binary_sensor and the
emulated document cannot disagree about the same enum. Side effect worth naming in
the PR: preconditioning now reads as climate-on in HA too, which it should.

### `feat/ha-availability` — retained state plus an availability topic

Two halves of one fix; neither is correct alone.

The gateway registers a Last Will on `<state_topic_base>/availability` and every
discovery config — curated, generic and command — points at it, so when the
gateway dies without a clean disconnect the broker publishes `offline` and HA marks
the entities *unavailable* instead of leaving them showing whatever value happened
to be last. A clean `DISCONNECT` **suppresses the will by protocol**, so `Stop()`
publishes `offline` itself: without that, the tidy shutdown path is the one that
lies. Verified both ways against the broker — `docker compose stop` → `offline`,
`docker kill` → `offline`, start → `online`.

State was published unretained, so a broker or HA restart left every gateway
entity blank until the next tick. It is now retained — which is only safe *because*
of the availability topic, since a retained value from a dead gateway would
otherwise look current indefinitely. The retained availability payload matters in
the reverse order too: an HA that starts while the gateway is down learns that
immediately rather than trusting retained state.

### `fix/shutdown-ordering` — wait for the shutdown flush

`store.Persist` already saves a snapshot when its context is cancelled, but it was
started with a bare `go` and never waited on, so `main` returned and the process
exited while that save was still in flight. The "saved on shutdown" guarantee was
a coin flip, and losing it costs up to one persist interval (30s) of field values —
which is precisely what makes an asleep car's sensors read `unknown` after a
restart, the thing the snapshot exists to prevent.

Goroutines that flush on the way out now register with a `WaitGroup` waited on
last, after the deferred `Stop()`/`Close()` calls. Bounded at 5s: a wedged
goroutine must not stop the process from exiting, because the runtime answers that
with `SIGKILL`, which is strictly worse. Verified on the deployed build — the
snapshot's mtime matches the stop instant on a container that had been up nine
seconds, far short of the 30s tick.

### `feat/vehicle-data-seed` — a gated fetch of the real `vehicle_data`

**Depends on `fix/debug-gate`** (reuses `debugAllowed` and its token).

Telemetry is delta-only: a field the car has not changed since enrollment is never
sent at all, so slow-moving values — `charge_limit_soc` and `sentry_mode` before
their first change, `trim_badging`, a parked `odometer` — are *missing* from the
emulated document rather than merely stale. `Relay.VehicleData` fetches one real
Fleet API document, which seeds them; the stream keeps them current afterwards.

Deliberate limits, all of them load-bearing:

- **It does not wake the car.** Tesla answers 408 for a sleeping vehicle and that
  surfaces as an error. Waking a car spends range on something a seed can wait for.
- **Nothing calls it on a timer.** Each call costs ~$0.002, so it is a manual
  `GET /debug/upstream/{vin}` behind the `/debug/state` gate, logged at warn level.
- **The VIN must already be known** to the gateway. Accepting an arbitrary VIN
  would turn a debug endpoint into a way to spend money against someone else's car.
- **The endpoint list must be percent-encoded.** Sent with literal semicolons,
  Tesla reads only the first entry and returns a document containing `charge_state`
  alone. Every other section comes back *absent*, which is indistinguishable from a
  car reporting nothing — a silent failure that produces a seed missing exactly the
  values the seed exists for. Measured, then fixed and tested.
- **`location_data` is not requested.** GPS arrives on the stream for free, and
  asking would tie the call to a scope it does not need.

The handler writes its response directly rather than through the shared response
helpers, which are a `Server` method on one branch and a package function on
another; it should not care which lands upstream first.

### `fix/rediscover-on-reconnect` — generic discovery must come back too

Discovery configs live on the broker as retained messages. When the broker loses
them — a fresh persistence database, a replaced container, retained messages
cleared by hand — the curated entities returned on the next reconnect, because the
connect handler clears `announced`. The generic per-field entities did not:
`discovered` was never cleared, so they were published exactly once per process and
a diagnostic entity could stay missing from Home Assistant until someone happened
to restart the gateway. One failure, two very different recovery times, for no
reason a user could see.

`resetAnnounced` now clears both maps. That puts a write on the MQTT connect
callback and reads on the publish ticker, so both go through the mutex that already
protected `announced` — `discovered` had been single-goroutine by accident, and
clearing it on reconnect without the lock would have introduced a race.

Verified live by restarting the broker: all 196 retained configs came back, every
one carrying `availability_topic`, with the availability topic back to `online`.

### `fix/dep-bump` — close three reachable advisories

`govulncheck` reported three vulnerabilities in code paths this project actually
calls: `GO-2025-4173` (paho.mqtt string encoding), `GO-2025-3503` (x/net proxy
bypass via IPv6 zone IDs) and `GO-2026-5970` (x/text infinite loop). The `x/`
packages were still on mid-2024 releases. Now clean.

### `fix/ci` — run CI on push and pull request, with a blocking linter

Plus `.golangci.yml`. `errcheck` is enabled and the lint job is a required check:
an advisory linter is a linter that rots. The exemptions are limited to
unactionable `Close` calls on HTTP response bodies and websockets — deliberately
narrower than golangci-lint's `std-error-handling` exclusion preset, which waives
all of `.*Close` / `.*Flush` / `os.Setenv` and would re-admit exactly the
discarded errors `fix/error-handling` fixed.

`gofmt` is not part of the lint job. The tree carries pre-existing formatting
drift in files nobody is touching; reformatting it wholesale would conflict with
every future upstream merge, so formatting is held per change — CI checks only
the files a commit touches, and `hack/gate.sh` asserts a change adds no *new*
drift.

This branch depends on `fix/error-handling`: the lint job only passes once the
errors it enforces are actually fixed.

## Local-only additions

`hack/gate.sh` is ours and is not proposed upstream, because it hard-codes
assumptions about our deployment.

It exists because **none of this project's real production failures were test
failures**. They were: a wedged ZMQ socket that kept publishing stale values, a
`/healthz` that returns a hard-coded `ok` and stayed green throughout, and a
container that built successfully while still serving the previous image. So the
gate has a static half (fmt, vet, `test -race`, `govulncheck`, `golangci-lint`,
image build) and a live half that proves the *deployed* process actually ingests
and serves: vehicle count, `car_type` matched against VIN position 4, `/healthz`
returning ready, `last_ingest_unix` present for every vehicle, no HA enum warning
spam, and an opt-in regression test that restarts the telemetry container and
asserts the gateway re-dials.

A "missing" `/healthz` document is called out separately from a 503, because it
means an old build is deployed — image built, container still serving the previous
one, which is half of why this script exists.

## Attribution and licence

MIT, unchanged, upstream `LICENSE` retained. Upstream commits keep their original
authorship; everything added here is a separate commit. If upstream picks these
changes up, the corresponding `fix/` branch disappears from this list on the next
merge — which is the goal.

## Deployment notes that are not upstream's problem

The container runs as uid 65532 (distroless nonroot). A credential file owned by
another uid with mode 0600 is therefore unreadable to it, and because config load
aborts when a `*_FILE` path cannot be read, the result is a restart loop rather
than a degraded start. Grant the container's gid read access — `chgrp 65532` plus
`chmod 640`, which keeps the file out of reach of other users while never making it
world-readable — or leave the value inline. Do not "fix" it by making the file
world-readable without deciding that deliberately.

`/data` is owned by 65532 as well, so pointing the container at a different uid is
not an alternative: every `state.json` and refresh-token write would start failing.

## Publishing

This fork is published under **sqmagellan** (`sqmagellan@gmail.com`), which is the
authorship on every non-upstream commit here.

Upstream commits keep their ORIGINAL hashes. When the placeholder authorship was
rewritten, the first attempt ran `git filter-branch` over `--all` and silently
rewrote upstream history too: filter-branch drops `gpgsig`, so every signed
upstream commit got a new SHA and `upstream-main` stopped being a mirror that can
`merge --ff-only upstream/main`. If you ever rewrite history here, scope it:

```bash
git filter-branch -f --env-filter ... -- --all --not <upstream-main tip>
```

and check `git rev-parse upstream-main` is unchanged afterwards.
