# community-teslafleet

Tesla Fleet Telemetry for Home Assistant and TeslaMate, self-hosted.

This is the maintained fork of [LasseLegarth/community-teslafleet](https://github.com/LasseLegarth/community-teslafleet), which has had no commits since July 2026. It fixes the bugs reported there and many others; see [CHANGELOG.md](CHANGELOG.md). The original design and most of the code are the original author's. The MIT license and the original commit authorship are unchanged.

## What it does

- **Home Assistant**: every signal the car streams becomes an MQTT discovery entity: sensors, covers, climate, locks and a device tracker.
- **Commands**: charging, climate, locks, windows, frunk and trunk, seat heaters, Sentry and navigation. Commands for a car run one at a time, with per-car limits.
- **TeslaMate**: a local Fleet API and streaming WebSocket. An unmodified TeslaMate logs from the stream instead of Tesla's billed API.

Reading data never wakes the car. Commands do, and Tesla bills wakes and commands. Streaming alone usually stays within Tesla's free monthly credit.

## How it works

```
Tesla car ──mTLS──▶ fleet-telemetry ──ZMQ──▶ community-teslafleet
                                                │
                    ┌───────────────────────────┴──────────────┐
                    ▼                                          ▼
          Home Assistant (MQTT)                    Fleet API + WSS ──▶ TeslaMate
          entities and commands                    local, optional
```

## Requirements

| | |
|---|---|
| Tesla developer app | Free at [developer.tesla.com](https://developer.tesla.com). |
| Public domain with HTTPS | Tesla fetches your public key from it. |
| Open TCP port | The car connects to it from the internet. See [Exposing the endpoints](#exposing-the-endpoints). |
| Fleet Telemetry support | Most cars on 2021 or newer firmware. |
| A host | Home Assistant OS or Supervised for the add-on; otherwise Docker. |

Then Home Assistant with an MQTT broker, TeslaMate, or both. The add-on finds Home Assistant's broker on its own.

The domain and the open port are Tesla's requirements. If you cannot meet them, [Teslemetry](https://teslemetry.com) is a hosted alternative.

## Install

**Home Assistant add-on.** Settings > Add-ons > Add-on Store > ⋮ > Repositories. Add `https://github.com/sqmagellan/community-teslafleet`, install Community TeslaFleet, and open it from the sidebar. The add-on reads its certificate from Home Assistant's `/ssl` folder, where the Let's Encrypt and DuckDNS add-ons put theirs. The onboarding wizard covers the keypair, partner registration, key pairing and enrollment.

The add-on installs separately from upstream's and does not take over its data.

**Docker.** `docker-compose.yml` runs fleet-telemetry, the vehicle-command proxy and the gateway, built from this tree:

```bash
cp .env.example .env
cp fleet-telemetry-config.example.json fleet-telemetry-config.json
docker compose up -d
```

Set `TGW_ONBOARD_ENABLED=true` to run the wizard on port 8099.

**TeslaMate.** Set `TESLA_API_HOST=http://<gateway-host>:4460` and `TESLA_WSS_HOST=ws://<gateway-host>:4460`. Keep `TESLA_AUTH_HOST` and your token. Port 4460 has no authentication and serves live location, so keep it on your LAN.

## Exposing the endpoints

Two things must be reachable from the internet.

**Public key.** Tesla fetches `https://<domain>/.well-known/appspecific/com.tesla.3p.public-key.pem`. Any HTTPS host on your domain works. The add-on serves the file on port 8098, so a reverse proxy or a Cloudflare Tunnel can route that path to it.

**Telemetry port.** The car connects with mutual TLS: it presents a client certificate that fleet-telemetry checks. A proxy that terminates TLS, including Cloudflare Tunnel, cannot carry that connection. Forward the port on your router to the host. Behind CGNAT, you need a host with a public IP that forwards raw TCP.

The server certificate can come from Let's Encrypt. DNS-01 validation avoids opening port 80. Tesla requires the full chain in the enrollment config, even for a publicly trusted certificate; the wizard asks for it.

## Units, privacy and backup

Set `units.system` to `metric` or `imperial`. Entity IDs use the car's name, not the VIN: `sensor.tesla_<name>_*`.

The add-on keeps its state in `/data`, which Home Assistant backups include. With Docker, back up `./data`, `./proxy`, `./certs`, `.env` and your config files.

## File permissions (Docker)

The gateway runs as uid 65532. A credential file mounted with `*_FILE` must be readable by that uid or group. Use `chgrp 65532` and `chmod 640`. An unreadable file stops the gateway at startup instead of starting without the credential.

## Support

Report bugs in this repository's issues. Include the add-on or image version and the relevant log lines, with VINs and tokens removed.

## License

MIT; see [LICENSE](LICENSE). Not affiliated with Tesla, Inc.
