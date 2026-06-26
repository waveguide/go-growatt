package main

import "testing"

func TestParsePowerRate(t *testing.T) {
	tests := []struct {
		payload  string
		wantRate uint16
		wantOK   bool
	}{
		{"0", 0, true},
		{"50", 50, true},
		{"100", 100, true},
		{" 75 ", 75, true}, // surrounding whitespace is trimmed
		{"\n42\n", 42, true},
		{"101", 0, false}, // above range
		{"-1", 0, false},  // below range
		{"", 0, false},    // empty
		{"abc", 0, false}, // not a number
		{"5.0", 0, false}, // not an integer
		{"50%", 0, false}, // trailing junk
	}

	for _, tt := range tests {
		t.Run(tt.payload, func(t *testing.T) {
			rate, ok := parsePowerRate(tt.payload)
			if ok != tt.wantOK || rate != tt.wantRate {
				t.Errorf("parsePowerRate(%q) = (%d, %t), want (%d, %t)",
					tt.payload, rate, ok, tt.wantRate, tt.wantOK)
			}
		})
	}
}
