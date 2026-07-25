// Package vin decodes Tesla vehicle identification numbers.
//
// Only the parts of a VIN that are specified rather than guessed are decoded:
// the model line (position 4), the ISO 3779 check digit (position 9), the model
// year (position 10) and the assembly plant (position 11). Trim level, paint
// and option packages are NOT encoded in a Tesla VIN in any stable, documented
// way, so this package deliberately does not try to infer them -- a confidently
// wrong car_type is worse than an absent one, because callers cannot tell it is
// wrong.
//
// The check digit matters as much as the model: it is what lets a caller
// distinguish a real VIN from a typo before trusting anything derived from it.
package vin

import "strings"

// Length is the number of characters in a valid VIN.
const Length = 17

// Info is the decoded content of a VIN. Every field is independently
// best-effort: anything that could not be decoded is left at its zero value so
// a caller can fall back to explicit configuration.
type Info struct {
	// VIN is the normalized (upper-cased, separator-stripped) input.
	VIN string

	// CheckDigitOK reports whether position 9 matches the ISO 3779 checksum.
	// False means the VIN is malformed -- truncated, mistyped, or not a VIN.
	// The remaining fields may still be populated, but should not be trusted
	// without operator confirmation.
	CheckDigitOK bool

	// CarType is the Tesla Fleet API vehicle_config.car_type value, e.g.
	// "modely". Empty when position 4 is not a model line we recognize.
	CarType string

	// ModelName is a human-readable label for the same model, e.g. "Model Y".
	ModelName string

	// ModelYear is the calendar year decoded from position 10, or 0 if the
	// character is not a legal year code.
	ModelYear int

	// Plant is the assembly plant from position 11, or "" if unrecognized.
	Plant string

	// Region describes the manufacturer and plant identified by the world
	// manufacturer identifier (positions 1-3), or "" if unrecognized.
	Region string
}

// Trusted reports whether the VIN was well formed AND yielded a known car
// type, i.e. whether CarType can be used without operator confirmation.
func (i Info) Trusted() bool { return i.CheckDigitOK && i.CarType != "" }

// model maps VIN position 4 to the Fleet API car_type and a display name.
//
// Tesla Semi is deliberately absent: its VIN layout is not publicly documented,
// so any mapping would be a guess. An unknown model line yields an empty
// CarType, which callers treat as "fall back to configuration".
var model = map[byte]struct{ carType, name string }{
	'S': {"models", "Model S"},
	'3': {"model3", "Model 3"},
	'X': {"modelx", "Model X"},
	'Y': {"modely", "Model Y"},
	'C': {"cybertruck", "Cybertruck"},
	'R': {"roadster", "Roadster"},
}

// wmi maps the world manufacturer identifier (positions 1-3) to a description.
var wmi = map[string]string{
	"5YJ": "Tesla, Fremont, California, USA",
	"7SA": "Tesla, Austin, Texas, USA",
	"7G2": "Tesla, Austin, Texas, USA",
	"LRW": "Tesla, Shanghai, China",
	"XP7": "Tesla, Berlin-Brandenburg, Germany",
}

// plant maps VIN position 11 to the assembly plant.
var plant = map[byte]string{
	'F': "Fremont, California",
	'A': "Austin, Texas",
	'B': "Berlin-Brandenburg, Germany",
	'C': "Shanghai, China",
	'N': "Reno, Nevada",
	'P': "Palo Alto, California",
}

// translit holds the ISO 3779 check-digit values for each legal VIN character.
// I, O and Q are absent because they are not legal in a VIN -- they are too
// easily confused with 1 and 0.
var translit = map[byte]int{
	'0': 0, '1': 1, '2': 2, '3': 3, '4': 4, '5': 5, '6': 6, '7': 7, '8': 8, '9': 9,
	'A': 1, 'B': 2, 'C': 3, 'D': 4, 'E': 5, 'F': 6, 'G': 7, 'H': 8,
	'J': 1, 'K': 2, 'L': 3, 'M': 4, 'N': 5, 'P': 7, 'R': 9,
	'S': 2, 'T': 3, 'U': 4, 'V': 5, 'W': 6, 'X': 7, 'Y': 8, 'Z': 9,
}

// weight holds the ISO 3779 positional weights. Position 9 (the check digit
// itself) has weight 0 so it does not contribute to its own checksum.
var weight = [Length]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}

// Normalize upper-cases a VIN and strips the separators people paste along with
// it. It does not validate.
func Normalize(raw string) string {
	repl := strings.NewReplacer(" ", "", "-", "", "\t", "")
	return strings.ToUpper(repl.Replace(strings.TrimSpace(raw)))
}

// Decode parses a VIN. It never returns an error: a VIN that cannot be decoded
// yields an Info with CheckDigitOK false and empty derived fields, which is
// exactly what a caller needs in order to fall back to configuration.
func Decode(raw string) Info {
	v := Normalize(raw)
	info := Info{VIN: v}
	if len(v) != Length || !legal(v) {
		return info
	}
	info.CheckDigitOK = checkDigit(v) == v[8]
	if m, ok := model[v[3]]; ok {
		info.CarType, info.ModelName = m.carType, m.name
	}
	info.ModelYear = modelYear(v[9])
	info.Plant = plant[v[10]]
	info.Region = wmi[v[:3]]
	return info
}

// CarType returns just the Fleet API car_type for a VIN, or "" when it cannot
// be determined from a well-formed VIN. A VIN that fails the check digit
// returns "" even if position 4 looks like a known model, because a malformed
// VIN is not evidence of anything.
func CarType(raw string) string {
	if i := Decode(raw); i.Trusted() {
		return i.CarType
	}
	return ""
}

// legal reports whether every character is a legal VIN character.
func legal(v string) bool {
	for i := 0; i < len(v); i++ {
		if _, ok := translit[v[i]]; !ok {
			return false
		}
	}
	return true
}

// checkDigit computes the ISO 3779 check digit for a normalized 17-character
// VIN. The result is a single character: "0"-"9" or "X" for a remainder of 10.
func checkDigit(v string) byte {
	sum := 0
	for i := 0; i < Length; i++ {
		sum += translit[v[i]] * weight[i]
	}
	r := sum % 11
	if r == 10 {
		return 'X'
	}
	return byte('0' + r)
}

// modelYear decodes position 10.
//
// The VIN year code cycles every 30 years, so a letter is ambiguous in the
// abstract: "L" is both 1990 and 2020. Tesla delivered its first car in 2008,
// so the letters are resolved on the 2010-2039 cycle and the digits 1-9 on
// 2001-2009 (which covers the original Roadster). This will need revisiting in
// 2040, by which time the VIN standard will be someone else's problem.
func modelYear(c byte) int {
	if c >= '1' && c <= '9' {
		return 2000 + int(c-'0')
	}
	const codes = "ABCDEFGHJKLMNPRSTVWXY"
	if i := strings.IndexByte(codes, c); i >= 0 {
		return 2010 + i
	}
	return 0
}
