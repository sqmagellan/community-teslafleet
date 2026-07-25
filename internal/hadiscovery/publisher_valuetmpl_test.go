package hadiscovery

import "testing"

// An enum sensor whose key is absent from the payload must render as the Jinja
// literal none (-> HA "unknown"), never as the empty string: HA validates enum
// state against the declared options list and logs a warning per message when a
// value is not a member. See valueTemplate.
func TestValueTemplateEnumRendersNone(t *testing.T) {
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
			name: "non-enum keeps empty string",
			e:    entity{Key: "soc", DeviceClass: "battery"},
			want: "{{ value_json.soc | default('') }}",
		},
		{
			name: "no device_class keeps empty string",
			e:    entity{Key: "odometer"},
			want: "{{ value_json.odometer | default('') }}",
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
