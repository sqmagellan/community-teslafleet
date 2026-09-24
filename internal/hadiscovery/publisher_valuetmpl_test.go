package hadiscovery

import "testing"

// A sensor whose key is absent from the payload must render as the Jinja literal
// none (-> HA "unknown"), never as the empty string. HA keeps the old state of a
// numeric sensor on an empty one, and rejects it for an enum. See valueTemplate.
func TestValueTemplateAbsentKeyRendersNone(t *testing.T) {
	p := &Publisher{}
	tests := []struct {
		name string
		e    entity
		want string
	}{
		{
			name: "enum uses none",
			e:    entity{Key: "detailed_charge_state", DeviceClass: "enum"},
			want: "{{ value_json.detailed_charge_state | default(none) }}",
		},
		{
			name: "numeric uses none",
			e:    entity{Key: "soc", DeviceClass: "battery"},
			want: "{{ value_json.soc | default(none) }}",
		},
		{
			name: "no device_class uses none",
			e:    entity{Key: "odometer"},
			want: "{{ value_json.odometer | default(none) }}",
		},
		{
			name: "explicit template wins",
			e:    entity{Key: "x", DeviceClass: "enum", ValueTmpl: "{{ value_json.y }}"},
			want: "{{ value_json.y }}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.valueTemplate(tt.e); got != tt.want {
				t.Errorf("valueTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}
