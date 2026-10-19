# Vehicle data templates

The emulated Fleet API lays live telemetry over a captured `vehicle_data`
response. Fields that rarely change, such as trim, color, wheels and GUI units,
come from the template. Location, battery, range and charge are live. Without a
template, a minimal skeleton is used, and `car_type` comes from the VIN.

## Capture once

With a valid Fleet API token, while the car is awake:

```bash
curl -s \
  -H "Authorization: Bearer $TESLA_TOKEN" \
  "https://fleet-api.prd.<region>.vn.cloud.tesla.com/api/1/vehicles/<id>/vehicle_data?endpoints=charge_state;climate_state;drive_state;gui_settings;vehicle_config;vehicle_state;location_data" \
  > templates/<VIN>.json
```

The file may be the bare response object or `{"response": {...}}`. Both work.

Capture again after a major firmware update, or enroll the `Version` field to
keep `car_version` live.

Files here are ignored by git, except `*.example.json`, because they contain your VIN.
