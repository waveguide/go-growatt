package inverter

import "testing"

func TestParseStats(t *testing.T) {
	// A block of 41 input registers (indices 0-40). Several 32-bit fields use a
	// non-zero high word so an incorrect high/low register index.
	data := []uint16{
		1,    // 0:  state -> "normal"
		0,    // 1:  PVInputPower hi
		1000, // 2:  PVInputPower lo      -> 100
		2350, // 3:  PV1InputVolt         -> 235
		105,  // 4:  PV1InputCurrent      -> 10
		1,    // 5:  PV1InputWatt hi
		1000, // 6:  PV1InputWatt lo      -> 6653
		2360, // 7:  PV2InputVolt         -> 236
		110,  // 8:  PV2InputCurrent      -> 11
		0,    // 9:  PV2InputWatt hi
		5500, // 10: PV2InputWatt lo      -> 550
		0,    // 11: ACWatt hi
		9000, // 12: ACWatt lo            -> 900
		5000, // 13: ACFrequency          -> 50
		2300, // 14: AC1Volt              -> 230
		200,  // 15: AC1Current           -> 20
		0,    // 16: AC1Watt hi
		8000, // 17: AC1Watt lo           -> 800
		2310, // 18: AC2Volt              -> 231
		210,  // 19: AC2Current           -> 21
		0,    // 20: AC2Watt hi
		8100, // 21: AC2Watt lo           -> 810
		2320, // 22: AC3Volt              -> 232
		220,  // 23: AC3Current           -> 22
		0,    // 24: AC3Watt hi
		8200, // 25: AC3Watt lo           -> 820
		0,    // 26: TotalTodayWatt hi
		50,   // 27: TotalTodayWatt lo    -> 5000
		1,    // 28: TotalAllTimeWatt hi
		0,    // 29: TotalAllTimeWatt lo  -> 6553600
		0,    // 30: TotalWorkTimeSecs hi
		7200, // 31: TotalWorkTimeSecs lo -> 3600
		450,  // 32: Temperature          -> 45
		0,    // 33: unused
		0,    // 34: unused
		0,    // 35: unused
		0,    // 36: unused
		0,    // 37: unused
		0,    // 38: unused
		0,    // 39: unused
		7,    // 40: FaultCode            -> 7
	}

	want := Stats{
		Timestamp:         "2024-01-02 03:04:05",
		State:             "normal",
		PVInputPower:      100,
		PV1InputVolt:      235,
		PV1InputCurrent:   10,
		PV1InputWatt:      6653,
		PV2InputVolt:      236,
		PV2InputCurrent:   11,
		PV2InputWatt:      550,
		ACWatt:            900,
		ACFrequency:       50,
		AC1Volt:           230,
		AC1Current:        20,
		AC1Watt:           800,
		AC2Volt:           231,
		AC2Current:        21,
		AC2Watt:           810,
		AC3Volt:           232,
		AC3Current:        22,
		AC3Watt:           820,
		TotalTodayWatt:    5000,
		TotalAllTimeWatt:  6553600,
		TotalWorkTimeSecs: 3600,
		Temperature:       45,
		FaultCode:         7,
	}

	got := parseStats(data, "2024-01-02 03:04:05")
	if got != want {
		t.Errorf("parseStats() mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestParseStatsUnknownState(t *testing.T) {
	data := make([]uint16, 41)
	data[0] = 99 // not in InverterStateCodes

	if got := parseStats(data, "ts").State; got != "" {
		t.Errorf("State for unknown code = %q, want empty string", got)
	}
}
