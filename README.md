# community-teslafleet

Bring your Tesla into Home Assistant, free and self-hosted.

## About this fork

A fork of [LasseLegarth/community-teslafleet](https://github.com/LasseLegarth/community-teslafleet) (MIT).
The design and almost all the code are the original author's; authorship and licence are
unchanged. Want the project itself? Start upstream.

Given upstream builds its image only on `v*` tags, `:latest` predates several important fixes,
including one where a dropped ZMQ peer wedges ingest while MQTT republishes stale values. We run
this against two cars, so patches live on branches, not in a dirty working tree. Each change is
one self-contained commit on its own `fix/…` branch cut from unmodified upstream, so each can go
upstream as its own PR. See [FORK.md](FORK.md).

## What it does

Turns a free Tesla Fleet Telemetry stream into Home Assistant MQTT auto-discovery. Every signal
the car emits becomes a real entity: sensors, covers, climate, device_tracker.
You also get command control of charging, climate, locks, windows, seat heaters, sentry and
navigation.

It never wakes the car, and costs nothing beyond Tesla's free monthly credit.

**TeslaMate users:** the gateway also serves a local Fleet API and streaming WebSocket, so an
unmodified [TeslaMate](https://github.com/teslamate-org/teslamate) keeps logging. It polls you
instead of Tesla's billed API, and Sentry mode stops mattering.

## How it works

```
   Tesla car ──mTLS──▶ fleet-telemetry ──ZMQ──▶ community-teslafleet
                                     (brokerless)        │
        ┌───────────────────────────────────────────────┤
        ▼                                                ▼
  Home Assistant  ◀── MQTT auto-discovery        Fleet API + WSS ──▶ TeslaMate
  (all entities + commands)                      (free local polling, optional)
```

Tesla bills `vehicle_data` polling, and a Sentry-on car stays awake polling forever, so the bill
climbs. Streaming is essentially free. Ingest is brokerless, so there's no broker to run.

## Requirements

Tesla's side, either way:

| | Why |
|---|---|
| A Tesla developer app | Free at [developer.tesla.com](https://developer.tesla.com). |
| A public domain with HTTPS | Tesla fetches your partner key; the car connects to a TLS hostname. |
| A reachable telemetry endpoint | The car dials in. Any public port, or a Cloudflare Tunnel behind CGNAT. |
| A Fleet-Telemetry-capable Tesla | Most cars on 2021 or newer firmware. |
| Somewhere to run it | Docker or LXC, or HA OS/Supervised for the add-on. |

Then pick at least one:

- **Home Assistant.** Needs HA and an MQTT broker. The add-on finds HA's broker for you.
- **TeslaMate only.** Needs TeslaMate, and no HA or MQTT at all.

The domain and public reachability are Tesla's requirements, not this project's, so they can only
be eased. Can't host that? [Teslemetry](https://teslemetry.com) is simpler.

## Install

**HA add-on (recommended).** Settings → Add-ons → ⋮ → Repositories, add
`https://github.com/LasseLegarth/community-teslafleet`, install Community TeslaFleet, and
configure it. Requires HA OS or Supervised. On HA Container or Core, use standalone.

**Standalone (Docker/LXC).** Brings up `fleet-telemetry`, `vehicle-command-proxy` and the gateway:

```bash
cp .env.example .env        # domain, VIN, HA broker
cp fleet-telemetry-config.example.json fleet-telemetry-config.json
docker compose up -d
```

Then run the onboarding wizard with `TGW_ONBOARD_ENABLED=true` on `:8099`. It generates the
signing keypair, publishes and verifies the `.well-known` key, registers the partner account, and
enrols telemetry.

For TeslaMate, set `TESLA_API_HOST=http://<gateway-host>:4460` and
`TESLA_WSS_HOST=ws://<gateway-host>:4460`. One port serves both the Fleet API and the legacy
`/streaming/` WebSocket. Keep `TESLA_AUTH_HOST` and your token.

## Exposing the telemetry endpoint

The car dials in from the internet, so pick one.

**Port forward.** Forward an external port to the same port on the host, and point DNS at your
public IP. fleet-telemetry terminates TLS with a Let's Encrypt cert, so use certbot DNS-01 and
skip opening port 80. Needs a public IP, router access, and no CGNAT.

**Cloudflare Tunnel.** `cloudflared` connects outbound to Cloudflare, which publishes a hostname
tunnelling in. No open ports, no static IP, works behind CGNAT. Cloudflare terminates TLS, so
omit `ca` in the config. Less battle-tested, so confirm enrollment reaches `synced: true`.

Public IP and able to open a port? Forward it. Otherwise tunnel.

Your partner public key is just a static file at
`https://<domain>/.well-known/appspecific/com.tesla.3p.public-key.pem`. It can live anywhere on
your domain, not necessarily this server.

## Units, privacy and backup

Set `units.system` to `metric` or `imperial`. The VIN stays out of entity_ids, so you get
`sensor.tesla_<name>_*`.

Everything stateful lives in `/data`. HA add-on backups include it; standalone means backing up
the `./data` volume.

## License

MIT, see [LICENSE](LICENSE). Not affiliated with Tesla, Inc.
