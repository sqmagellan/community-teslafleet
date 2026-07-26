package fleetapi

import (
	"strings"
	"testing"
)

func TestRedactQuery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{
			// The real case: TeslaMate's streaming client sends its token here.
			name: "token is redacted, shape is kept",
			in:   "token=abc123&tag=5YJ",
			want: "tag=5YJ&token=%3Credacted%3E",
		},
		{"nothing sensitive is untouched", "tag=5YJ&interval=1", "interval=1&tag=5YJ"},
		{"oauth code is redacted", "code=NA_abc&state=xyz", "code=%3Credacted%3E&state=xyz"},
		{"client_secret is redacted", "client_secret=shhh", "client_secret=%3Credacted%3E"},
		{"matching is case-insensitive", "Token=abc123", "Token=%3Credacted%3E"},
		{
			name: "every value of a repeated param is redacted",
			in:   "token=one&token=two",
			want: "token=%3Credacted%3E&token=%3Credacted%3E",
		},
		{
			// If it cannot be parsed it cannot be redacted, so it must not be
			// logged at all.
			name: "unparseable query is dropped, not logged raw",
			in:   "token=abc%zz",
			want: "<unparseable, redacted>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactQuery(tt.in)
			if got != tt.want {
				t.Errorf("redactQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// Belt and braces: no test input's secret may survive in any form.
			for _, secret := range []string{"abc123", "NA_abc", "shhh", "one", "two"} {
				if strings.Contains(tt.in, secret) && strings.Contains(got, secret) {
					t.Errorf("redactQuery(%q) leaked %q: %q", tt.in, secret, got)
				}
			}
		})
	}
}
