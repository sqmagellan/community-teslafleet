# Changelog

## 0.2.1: the add-on can use a real certificate (2026-10-18)

- **Add-on certificate.** New `tls_cert` and `tls_key` options read a certificate from Home Assistant's `/ssl` folder, `fullchain.pem` and `privkey.pem` by default. The wizard sends that chain to Tesla without a paste. The add-on had no way to set a certificate, so the car could not connect to it.
- **Certificate warning.** Without a certificate, or with files that do not load as a certificate and key, the gateway warns on every start that the car will not accept the self-signed fallback. It used to call this "tunnel mode", but a tunnel cannot carry the car's connection.
- **Pinned Tesla images.** The add-on image and `docker-compose.yml` use fleet-telemetry v0.9.5 and vehicle-command 0.4.1 by tag and digest, so a rebuild ships the same binaries. They used `latest`.

## 0.2.0: first release of the maintained fork (2026-10-18)

Changes since upstream's last add-on image, 0.1.8.

**Commands**
- **One at a time.** Commands for a car run in arrival order. A waiting set-state command takes a newer value, so a slider ends where it was released. Concurrent commands used to race.
- **Per-car limits.** At most 10 of one command and 10 wakes an hour, and no repeat of a set-state command within 60 seconds. Each limit is configurable; 0 turns it off.
- **Strict payloads.** Each actuator accepts only payloads it knows. `LOCK` with a trailing newline used to unlock the car, and an unknown payload turned Sentry, valet mode or PIN to Drive off.
- **Frunk and trunk.** Both covers show real state, and a close command no longer opens the frunk.
- **Retained commands.** A retained message on a command topic is an old request. It is dropped.
- **Rate limits.** A 429 from Tesla is retried once, after the delay it names.
- **Bundled proxy.** The add-on starts the vehicle-command proxy it ships. Commands never worked in the add-on before.
- **One token owner.** Tesla rotates the refresh token on every use. With commands on, the wizard uses the relay's token instead of refreshing its own. A rotated token that could not be saved shows in `/healthz`.

**Home Assistant**
- **Locks.** A locked car showed as unlocked.
- **Availability.** Entities go unavailable when the gateway stops. State is retained, so it survives broker and Home Assistant restarts.
- **Missing values.** A sensor the car stops sending, or reports as invalid, shows as unknown instead of its last value. Switch, cover and lock states keep their last value through short gaps, such as a firmware install.
- **Enum sensors.** An absent value no longer logs a warning on every message.
- **Rediscovery.** Every entity returns after the broker loses its retained configs.
- **Charging and plug state.** Residual DC power no longer reads as charging. A stale charge-port latch no longer reports a cable.
- **Online sensor.** Follows the car's state. It read true for a sleeping car.

**TeslaMate**
- **Car model.** `car_type` comes from the VIN. It was hard-coded to Model 3.
- **Drive power.** Reported from pack voltage and current while driving. It was always 0. Requires `PackVoltage` and `PackCurrent` in the enrollment.
- **Battery level.** Displayed and usable charge are separate and rounded as the Fleet API rounds them, so the cold-battery indicator works.
- **Positions.** Frames carry the time of the observation. A stalled feed was stamped as a run of fresh positions at the end of a drive.
- **Climate.** `is_climate_on` is reported.
- **Discovered cars.** A car found on the stream no longer crashes `vehicle_data`, and it can stream.
- **Restarts.** A restored snapshot no longer holds a sleeping car online or in gear.

**Onboarding and add-on**
- **Enrollment.** Step 7 sends the `{vins, config}` envelope with the certificate chain Tesla requires. It used to crash the proxy ([upstream #1](https://github.com/LasseLegarth/community-teslafleet/issues/1)).
- **Fleet API URL.** Step 1 and the add-on options take the regional URL ([upstream #3](https://github.com/LasseLegarth/community-teslafleet/issues/3)).
- **Add-on options.** Client ID, client secret, refresh token and Fleet API URL can be set, so commands can be turned on.
- **Signing key.** Replacing the key asks for confirmation. Every paired car trusts the old one.

**Security**
- **Endpoints.** `/debug/state` is off by default. Enrollment and paid Fleet API fetches require a token.
- **Wizard.** Without a password, it answers only on loopback; Home Assistant ingress still works. Cross-site form posts are refused.
- **Secrets.** Any string setting can be read from `<VAR>_FILE`. Tokens are kept out of logs, and child processes no longer inherit credentials.
- **WebSocket.** A browser `Origin` must match the host.
- **Dependencies.** Go 1.26.9, go-chi 5.5.0 and current `golang.org/x` packages. govulncheck is clean.

**Reliability**
- **Health check.** `/healthz` reports readiness per car. It returned a fixed `ok`.
- **Startup and shutdown.** A down MQTT broker no longer blocks startup. The state snapshot is saved before exit.
- **Errors.** Write and flush failures are logged instead of dropped.
- **CI.** Tests, lint and govulncheck run on every push. A release publishes images only after its tests pass.

**Docs**
- **Cloudflare Tunnel.** It cannot carry the car's connection, which uses mutual TLS. The README said it could. It still works for the public key.

The detailed record of each change is [FORK.md as of 0.2.0](https://github.com/sqmagellan/community-teslafleet/blob/2f2c8b5/FORK.md).
