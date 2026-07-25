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

## Note for whoever publishes this

Commits here are authored as `Ames Homelab <ames@localhost>` — a deliberate
placeholder, because whether this fork carries a real name and where it is hosted
has not been decided. Set `user.name`/`user.email` and rewrite the authorship
before the first push if that matters to you.
