# Vehicle data templates

The Fleet API emulator overlays **live telemetry** on top of a **captured real
`vehicle_data` response**. Static fields (model, trim, color, wheels, gui units)
come from the template; dynamic fields (location, battery, range, charge, …) are
live. Without a template a minimal skeleton is used (works, but TeslaMate won't
know the exact model).

## Capture once

With a valid Fleet API token, while the car is awake:

```bash
curl -s \
  -H "Authorization: Bearer $TESLA_TOKEN" \
  "https://fleet-api.prd.<region>.vn.cloud.tesla.com/api/1/vehicles/<id>/vehicle_data?endpoints=charge_state;climate_state;drive_state;gui_settings;vehicle_config;vehicle_state;location_data" \
  > templates/<VIN>.json
```

The file may be the bare response object or `{"response": {...}}`. Both work.

Re-capture after a major firmware update (or enroll the `Version` telemetry field
to keep `car_version` live).

Files here are git-ignored (they contain your VIN) except `*.example.json`.
