# Community TeslaFleet

Brings your Tesla into Home Assistant through MQTT discovery. One container runs Tesla's fleet-telemetry server, the vehicle-command proxy and the gateway. It can also serve a local Fleet API for TeslaMate.

Open the add-on from the sidebar to run the onboarding wizard. It covers the keypair, partner registration, key pairing and enrollment. Home Assistant's MQTT broker is detected automatically.

## What you need

- **A Tesla developer app**, free at developer.tesla.com, for the client ID and secret.
- **A public domain with HTTPS.** Tesla fetches your public key from it. The add-on serves the key on port 8098; route your domain's `/.well-known/` path there.
- **An open telemetry port.** The car connects to it with mutual TLS. Forward the port on your router to Home Assistant. Cloudflare Tunnel cannot carry this connection.
- **A certificate for your domain in `/ssl`**, such as one from the Let's Encrypt or DuckDNS add-on. The add-on reads `fullchain.pem` and `privkey.pem` by default, and the wizard sends the chain to Tesla. Without one, the add-on falls back to a self-signed certificate the car will not accept. Restart the add-on after the certificate renews.

## Options

| Option | Meaning |
|---|---|
| `namespace` | Telemetry namespace. Leave the default. |
| `units_system` | `metric` or `imperial`. Display only. |
| `device_identifier` | `name` keeps the VIN out of entity IDs. `vin` uses it. |
| `telemetry_port` | The port the car connects to. Default 4443. |
| `telemetry_profile` | The wizard's starting enrollment profile. Changing it does not re-enroll the car. |
| `fleetapi_enabled` | Turn on only if you run TeslaMate. |
| `vins` | Leave empty to find cars automatically, or list VINs to limit which are published. |
| `commands_enabled` | Lets Home Assistant control the car. Needs onboarding and `tesla_client_id`. |
| `tesla_client_id`, `tesla_client_secret` | Your developer app's credentials. |
| `tesla_refresh_token` | First start only, if you did not paste a token in the wizard. |
| `fleet_api_url` | Your region's Fleet API, for example `https://fleet-api.prd.na.vn.cloud.tesla.com`. |
| `tls_cert`, `tls_key` | Certificate and key files in `/ssl`. Defaults `fullchain.pem` and `privkey.pem`. |
| `log_level` | `debug`, `info`, `warn` or `error`. |

## Ports

- **4443/tcp, telemetry**: must be reachable from the internet. Change it with `telemetry_port`.
- **8098/tcp, public key**: route `https://<domain>/.well-known/` here.
- **4460/tcp, TeslaMate**: LAN only. No authentication, and it serves live location. Leave it unmapped unless TeslaMate runs on another host.

## Data

Keys, the enrollment config, certificates and the token cache live in `/data`. Home Assistant backups include it.

Report problems at https://github.com/sqmagellan/community-teslafleet/issues.
