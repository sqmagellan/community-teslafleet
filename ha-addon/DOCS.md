# Community TeslaFleet: Home Assistant add-on

Brings your Tesla into Home Assistant via MQTT auto-discovery. This add-on is
**self-contained**: it bundles the whole stack: Tesla **fleet-telemetry** (receives
the car's stream), the **vehicle-command proxy** (for commands), and the gateway,
in one container. You don't run fleet-telemetry separately. Optionally it also serves
a local Tesla **Fleet API + WSS** for TeslaMate (free polling that never wakes the car).

The gateway supervises the bundled processes and serves a guided **onboarding wizard**
in the HA sidebar (via ingress). Configure the rest in the **Configuration** tab.

## What you still need (Tesla's requirements, can't be removed)

- **A public domain + HTTPS** for your Tesla developer app's `.well-known` public key.
- **Public reachability for the telemetry port**: the car dials *in*. Either
  **port-forward** the telemetry port (below) on your router to Home Assistant, or run a
  **Cloudflare Tunnel**. Behind CGNAT, use the tunnel.
- **A Tesla developer app** (free at developer.tesla.com) gives you `client_id` + `client_secret`.

The onboarding wizard walks you through keys, partner registration, virtual-key
pairing, and enrollment. HA's MQTT broker is **auto-detected** from the Supervisor.

## Options

| Option | Meaning |
|---|---|
| `namespace` | telemetry namespace (leave default unless you have a reason) |
| `units_system` | `metric` (km/°C/bar) or `imperial` (mi/°F/psi), how HA displays units |
| `device_identifier` | `name` (slug, keeps the VIN out of entity_ids) or `vin` |
| `telemetry_port` | port the car connects to (port-forward this, or tunnel to it). Default `4443` |
| `telemetry_profile` | `eco` / `balanced` / `live` / `custom`, how often signals are sent |
| `fleetapi_enabled` | `true` only if you also run TeslaMate |
| `vins` | **optional**, leave empty to auto-discover your car(s); set to filter to specific VINs |
| `commands_enabled` | enable HA-to-car commands (starts the bundled proxy; needs onboarding) |
| `log_level` | debug / info / warn / error |

## Ports

- **`4443/tcp` (telemetry)**: the car connects here from the internet. **Must be
  reachable**: port-forward it on your router to Home Assistant, or use a Cloudflare
  Tunnel. Change the number with `telemetry_port`.
- **`4460/tcp` (Fleet API + WSS for TeslaMate)**: **LAN-only, never expose to the
  internet.** Leave unmapped unless you run TeslaMate on another host.

## Data

Keys, config, `ftc.json`, certs and the token cache persist in the add-on's `/data`
(included in HA backups).
