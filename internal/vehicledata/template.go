package vehicledata

import (
	"encoding/json"
	"fmt"
	"os"
)

// Template holds a captured real vehicle_data "response" object as raw JSON.
// Build() clones it fresh per request and overlays live telemetry, so static
// fields (vehicle_config, gui_settings) come from a real capture while dynamic
// fields are live. See templates/README for the capture command.
type Template struct {
	bytes []byte
}

// LoadTemplate reads a captured template. The file may be either the bare
// response object or wrapped as {"response": {...}}. A missing path yields a
// minimal default skeleton (TeslaMate works but won't know the exact model).
func LoadTemplate(path string) (*Template, error) {
	if path == "" {
		return &Template{bytes: defaultTemplate()}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return &Template{bytes: defaultTemplate()}, fmt.Errorf("template %s: %w (using default skeleton)", path, err)
	}
	// Unwrap {"response": {...}} if present.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return &Template{bytes: defaultTemplate()}, fmt.Errorf("template %s parse: %w (using default skeleton)", path, err)
	}
	if resp, ok := probe["response"]; ok {
		return &Template{bytes: resp}, nil
	}
	return &Template{bytes: b}, nil
}

// Clone returns a fresh deep copy of the template as a mutable map.
//
// A nil receiver yields the default skeleton. Templates are loaded per
// CONFIGURED VIN, but /api/1/vehicles also lists auto-discovered VINs, so a
// zero-config deployment looks one up and gets nil -- which used to panic the
// vehicle_data handler on the first request for a car nobody had configured.
func (t *Template) Clone() map[string]any {
	b := defaultTemplate()
	if t != nil {
		b = t.bytes
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func defaultTemplate() []byte {
	return []byte(`{
		"access_type": "OWNER",
		"in_service": false,
		"calendar_enabled": true,
		"api_version": 96,
		"drive_state": {},
		"charge_state": {},
		"climate_state": {},
		"gui_settings": {
			"gui_distance_units": "mi/hr",
			"gui_temperature_units": "C",
			"gui_charge_rate_units": "kW",
			"gui_range_display": "Rated",
			"gui_24_hour_time": true,
			"show_range_units": true
		},
		"vehicle_config": {},
		"vehicle_state": {
			"software_update": {"status": "", "download_perc": 0, "install_perc": 0}
		}
	}`)
}
