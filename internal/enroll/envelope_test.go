package enroll

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestWrap(t *testing.T) {
	bare := []byte(`{"hostname":"h","ca":"x","fields":{"Soc":{"interval_seconds":60}}}`)
	b, err := Wrap(bare, []string{"VIN1"})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		VINs   []string       `json:"vins"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(b, &env); err != nil || len(env.VINs) != 1 || env.Config["hostname"] != "h" {
		t.Errorf("Wrap = %s", b)
	}
	if again, _ := Wrap(b, nil); string(again) != string(b) {
		t.Error("an already wrapped payload was changed")
	}
	if _, err := Wrap(bare, nil); !errors.Is(err, ErrNoVINs) {
		t.Errorf("no VINs: %v", err)
	}
	if _, err := Wrap([]byte(`{"hostname":"h"}`), []string{"VIN1"}); err == nil {
		t.Error("a config with no fields was accepted")
	}
}

func TestHasCA(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`{"ca":"pem","fields":{}}`, true},
		{`{"fields":{}}`, false},
		{`{"vins":["V"],"config":{"ca":"pem"}}`, true},
		{`{"vins":["V"],"config":{"fields":{}}}`, false},
		{`nope`, false},
	} {
		if got := HasCA([]byte(c.in)); got != c.want {
			t.Errorf("HasCA(%s) = %v", c.in, got)
		}
	}
}
