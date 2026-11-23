# Changelog

## Unreleased

- **One naming scheme for Home Assistant entities.** **Entity IDs change.** The gateway clears the old discovery topics itself, so Home Assistant deletes the old entities. Automations that use them need the new IDs.
  - Fields without a curated entity were keyed by their Tesla name run together (`sensor.<car>_chargeenablerequest`), under `raw_<Field>` topics. They now follow the curated scheme: `charge_enable_request`, named "Charge enable request".
  - Binary sensors are named for the thing, and the device class supplies the state words:
    - `lock` ("Lock: Locked") replaces `locked` ("Locked: Unlocked").
    - `sentry_mode` replaces `sentry`.
    - `charge_cable` replaces `plugged_in`.
    - `driver_seat` replaces `driver_present`.
  - One entity per fact. `online`, `charging` and `at_home` are gone; use `state`, `charging_state` and the location tracker's zone. The sentry enum becomes `sentry_mode_state`. It, `soc_raw` and `charge_state_raw` are diagnostic and disabled by default.
  - On every announce the gateway publishes an empty retained config to each retired topic.
- **Entity IDs on Home Assistant 2026.4 and later.** Home Assistant stopped reading `object_id` in MQTT discovery, so entity IDs followed the device and entity names instead of the keys: the battery level became `sensor.<car>_battery`, not `sensor.<car>_battery_level`. Discovery now also sends `default_entity_id` (`<domain>.<car>_<key>`, lowercase), which names new entities as before. `object_id` stays for older versions. Entities that already exist keep their IDs.
- **Wizard client secret from config.** The wizard used the client secret only from its own form, so `TGW_TESLA_CLIENT_SECRET` and its `_FILE` form did nothing there and the secret had to be typed into a web page. It now uses the configured secret when none was saved in the wizard; a secret saved in the wizard still wins, as its Fleet API URL does.
- **Wizard with the public key only.** When the private key lives only with the vehicle-command proxy, as in `docker-compose.yml` (`./proxy/private-key.pem`), the wizard saw no keypair: step 2 offered "Generate keypair", and generating replaced the registered public key without the confirmation. The public key on disk now counts as an existing keypair.
- **The wizard enrolls the configured cars.** Step 7 enrolled every car on the account, so a gateway configured for some of them paid for streams from the others. With vehicles configured (`TGW_VINS`), it enrolls those that are on the account, and step 7 lists them; with none configured, every listed car, as before.
- **eco sends the car's state.** Fields that no category named fell into "diagnostic", which eco turns off, so eco never sent the odometer, sentry mode, seat belts or hazard lights, 47 fields in all. They are now a "state" category: eco sends each when it changes, at most every 5 minutes, and never resends it, which adds a few cents a month per car. Balanced and live keep their cadence. Enroll again (step 7) to apply it.
- **Telemetry config check.** Tesla deletes a car's telemetry config at the billing limit, and configs expire. Either way the car stops streaming and looks asleep. With `fleet_api_url` set, the gateway reads each configured car's config once a day (`telemetry_config_check_hours`) and reports it in `/healthz` as `telemetry_config`: `ok`, or the problem per car (`missing`, `limit_reached`, `expired`, `expires_in_3d`). The read does not wake the car.
- **The command proxy's certificate is checked.** The gateway sends your OAuth token to the vehicle-command proxy with every command and used to accept any certificate. It now checks the certificate and its host name against `proxy_ca_file` (`TGW_TESLA_PROXY_CA_FILE`). `docker-compose.yml` mounts `./proxy/tls/cert.pem` for this, and the certificate must name `vehicle-command-proxy`. The add-on trusts the certificate it makes for its own proxy. `proxy_insecure_skip_verify` turns checking off and logs a warning on every start. `/healthz` shows `commands_proxy`.
- **Number ranges.** Charge limit, charging amps, climate temperature and speed limit refuse values outside their range instead of passing them to the car. Charging amps uses the maximum the car reports (`ChargeCurrentRequestMax` telemetry, or a fetched `vehicle_data`), otherwise 48 A. It was advertised as 32 A, which capped 48 A cars. The charge limit uses the car's reported minimum and maximum when known, otherwise 50 to 100%. Home Assistant's sliders show the same ranges, and update when the car reports new ones.
- **Unknown, not false.** A lock, binary sensor or plugged-in state that has never had a reading shows as unknown. It used to show unlocked, closed or unplugged. Lock, switch and cover states still keep their last real value through a short gap, such as a firmware install.
- **Drives with a stuck gear.** A car can report P and then drive with its gear never leaving P while speed and location keep streaming. TeslaMate saw a parked car and lost the whole drive. A speed of 3 or more that arrives after the last gear reading now counts as driving, and the car is reported in D. A later P or an active charge ends it. Home Assistant's shift state sensor reports D too.

- **Command guards (review 2026-11-16).** Remote start needs the button's `PRESS` and a software install needs `INSTALL`; any payload used to run them. A VIN that is not letters and digits is refused before it reaches a URL. `NaN` and infinity are refused as numbers. The speed limit value needs the speed-limit PIN, like the speed limit switch. The trunk does not toggle on state restored from disk after a restart. A car's command queue holds at most 32 waiting commands.
- **Command topics.** A `state_topic_base` with more than one level (`teslafleet/home`) dropped every command. Two cars whose names give the same id (`Road Car`, `Road-Car`) are refused instead of sending to the first.
- **Wizard.** Requests from a sibling subdomain are refused like cross-site ones. Generating a key asks for confirmation whenever a private key file exists. Pasting a refresh token with no `token_cache` path is an error instead of "saved". Saves no longer share one temp file.

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
