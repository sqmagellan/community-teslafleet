package vin

import "testing"

// All VINs in this file are SYNTHETIC. They carry valid ISO 3779 check digits
// so the checksum path is exercised for real, but they do not identify any
// vehicle. Never commit a real VIN to this repository: a VIN identifies a
// specific car and, through it, its owner.
func TestDecode(t *testing.T) {
	tests := []struct {
		name      string
		vin       string
		carType   string
		modelName string
		year      int
		plant     string
		checkOK   bool
		trusted   bool
	}{
		{
			name: "Model S Fremont 2015", vin: "5YJSA1E22FF101234",
			carType: "models", modelName: "Model S", year: 2015,
			plant: "Fremont, California", checkOK: true, trusted: true,
		},
		{
			name: "Model 3 Fremont 2019", vin: "5YJ3E1EA2KF301234",
			carType: "model3", modelName: "Model 3", year: 2019,
			plant: "Fremont, California", checkOK: true, trusted: true,
		},
		{
			name: "Model X Fremont 2017", vin: "5YJXCDE23HF201234",
			carType: "modelx", modelName: "Model X", year: 2017,
			plant: "Fremont, California", checkOK: true, trusted: true,
		},
		{
			name: "Model Y Fremont 2020", vin: "5YJYGDEF5LF001234",
			carType: "modely", modelName: "Model Y", year: 2020,
			plant: "Fremont, California", checkOK: true, trusted: true,
		},
		{
			name: "Model Y Austin 2023", vin: "7SAYGDEF4PA101234",
			carType: "modely", modelName: "Model Y", year: 2023,
			plant: "Austin, Texas", checkOK: true, trusted: true,
		},
		{
			// Check digit "X" (remainder 10) -- the case a naive
			// implementation gets wrong by emitting "10" or failing.
			name: "Model Y Berlin 2022 with X check digit", vin: "XP7YGCELXNB101234",
			carType: "modely", modelName: "Model Y", year: 2022,
			plant: "Berlin-Brandenburg, Germany", checkOK: true, trusted: true,
		},
		{
			name: "Model Y Shanghai 2021", vin: "LRWYGCEK8MC101234",
			carType: "modely", modelName: "Model Y", year: 2021,
			plant: "Shanghai, China", checkOK: true, trusted: true,
		},
		{
			name: "Cybertruck Austin 2024", vin: "7G2CEHED0RA101234",
			carType: "cybertruck", modelName: "Cybertruck", year: 2024,
			plant: "Austin, Texas", checkOK: true, trusted: true,
		},
		{
			// Year digit rather than letter, and an unmapped plant code.
			name: "Roadster 2008", vin: "5YJRE11B88V100123",
			carType: "roadster", modelName: "Roadster", year: 2008,
			plant: "", checkOK: true, trusted: true,
		},
		{
			// Well-formed VIN, but position 4 is not a model we know: we must
			// report no car type rather than guess one.
			name: "unknown model line", vin: "5YJWA1E20FF101234",
			carType: "", modelName: "", year: 2015,
			plant: "Fremont, California", checkOK: true, trusted: false,
		},
		{
			// One digit off. Everything else still decodes, but Trusted must be
			// false so callers do not act on it.
			name: "bad check digit", vin: "5YJYGDEF6LF001234",
			carType: "modely", modelName: "Model Y", year: 2020,
			plant: "Fremont, California", checkOK: false, trusted: false,
		},
		{
			name: "too short", vin: "5YJYGDEF5LF00123",
			carType: "", modelName: "", year: 0, plant: "", checkOK: false, trusted: false,
		},
		{
			name: "empty", vin: "",
			carType: "", modelName: "", year: 0, plant: "", checkOK: false, trusted: false,
		},
		{
			// I, O and Q are not legal VIN characters.
			name: "illegal character", vin: "5YJQGDEF5LF001234",
			carType: "", modelName: "", year: 0, plant: "", checkOK: false, trusted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decode(tt.vin)
			if got.CarType != tt.carType {
				t.Errorf("CarType = %q, want %q", got.CarType, tt.carType)
			}
			if got.ModelName != tt.modelName {
				t.Errorf("ModelName = %q, want %q", got.ModelName, tt.modelName)
			}
			if got.ModelYear != tt.year {
				t.Errorf("ModelYear = %d, want %d", got.ModelYear, tt.year)
			}
			if got.Plant != tt.plant {
				t.Errorf("Plant = %q, want %q", got.Plant, tt.plant)
			}
			if got.CheckDigitOK != tt.checkOK {
				t.Errorf("CheckDigitOK = %v, want %v", got.CheckDigitOK, tt.checkOK)
			}
			if got.Trusted() != tt.trusted {
				t.Errorf("Trusted() = %v, want %v", got.Trusted(), tt.trusted)
			}
		})
	}
}

// CarType is the accessor the mapper uses, so pin its contract separately:
// it must stay silent rather than guess on anything it cannot verify.
func TestCarType(t *testing.T) {
	tests := []struct {
		vin  string
		want string
	}{
		{"5YJYGDEF5LF001234", "modely"},
		{"5YJ3E1EA2KF301234", "model3"},
		{"5YJYGDEF6LF001234", ""}, // bad check digit -> refuse to guess
		{"5YJWA1E20FF101234", ""}, // unknown model line
		{"", ""},
		{"not a vin", ""},
	}
	for _, tt := range tests {
		if got := CarType(tt.vin); got != tt.want {
			t.Errorf("CarType(%q) = %q, want %q", tt.vin, got, tt.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"5yjygdef5lf001234", "5YJYGDEF5LF001234"},
		{"  5YJYGDEF5LF001234  ", "5YJYGDEF5LF001234"},
		{"5YJ-YGDEF5LF001234", "5YJYGDEF5LF001234"},
		{"5YJ YGDEF5 LF001234", "5YJYGDEF5LF001234"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// A lower-cased VIN must decode identically to its upper-cased form.
	if got := Decode("5yjygdef5lf001234"); !got.Trusted() || got.CarType != "modely" {
		t.Errorf("Decode(lowercase) = %+v, want trusted modely", got)
	}
}

// checkDigit is the part most likely to be subtly wrong, so verify it directly
// against VINs whose stated check digit we know is correct.
func TestCheckDigit(t *testing.T) {
	valid := []string{
		"5YJSA1E22FF101234",
		"5YJ3E1EA2KF301234",
		"5YJXCDE23HF201234",
		"5YJYGDEF5LF001234",
		"7SAYGDEF4PA101234",
		"XP7YGCELXNB101234",
		"LRWYGCEK8MC101234",
		"7G2CEHED0RA101234",
		"5YJRE11B88V100123",
	}
	for _, v := range valid {
		if got := checkDigit(v); got != v[8] {
			t.Errorf("checkDigit(%s) = %c, want %c", v, got, v[8])
		}
	}
}
