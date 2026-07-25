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

### `fix/dep-bump` — close three reachable advisories

`govulncheck` reported three vulnerabilities in code paths this project actually
calls: `GO-2025-4173` (paho.mqtt string encoding), `GO-2025-3503` (x/net proxy
bypass via IPv6 zone IDs) and `GO-2026-5970` (x/text infinite loop). The `x/`
packages were still on mid-2024 releases. Now clean.

### `fix/ci` — run CI on push and pull request

Plus `.golangci.yml`. The lint job is advisory for now: `errcheck` is the linter
this codebase most needs, but enabling it today means a permanently red build,
so it is deferred to the error-handling work rather than switched on and ignored.

## Local-only additions

`hack/gate.sh` is ours and is not proposed upstream, because it hard-codes
assumptions about our deployment.

It exists because **none of this project's real production failures were test
failures**. They were: a wedged ZMQ socket that kept publishing stale values, a
`/healthz` that returns a hard-coded `ok` and stayed green throughout, and a
container that built successfully while still serving the previous image. So the
gate has a static half (fmt, vet, `test -race`, `govulncheck`, image build) and a
live half that proves the *deployed* process actually ingests and serves —
including an opt-in regression test that restarts the telemetry container and
asserts the gateway re-dials.

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
