package xenserver

import (
	"reflect"
	"testing"
)

func TestParseIPMISensors_FullRow(t *testing.T) {
	raw := "CPU1 Temp        | 45.000     | degrees C  | ok    | 0.000     | 0.000     | 0.000     | 90.000    | 95.000    | 100.000\n"
	got := parseIPMISensors(raw)
	want := []IPMISensor{
		{
			Name: "CPU1 Temp", Reading: "45.000", Units: "degrees C", Status: "ok",
			LNR: "0.000", LCR: "0.000", LNC: "0.000", UNC: "90.000", UCR: "95.000", UNR: "100.000",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseIPMISensors() = %+v, want %+v", got, want)
	}
}

func TestParseIPMISensors_ShortRow(t *testing.T) {
	// Discrete sensors often have no threshold columns at all.
	raw := "Chassis Intru    | 0x0        | discrete   | 0x0080\n"
	got := parseIPMISensors(raw)
	if len(got) != 1 {
		t.Fatalf("got %d sensors, want 1", len(got))
	}
	if got[0].Name != "Chassis Intru" || got[0].Reading != "0x0" || got[0].Units != "discrete" || got[0].Status != "0x0080" {
		t.Errorf("unexpected parse: %+v", got[0])
	}
	if got[0].LNR != "" {
		t.Errorf("LNR should be empty for a short row, got %q", got[0].LNR)
	}
}

func TestParseIPMISensors_MultipleRowsAndNoise(t *testing.T) {
	raw := `CPU1 Temp        | 45.000     | degrees C  | ok
FAN1             | 2400.000   | RPM        | ok
not a sensor line without a pipe
`
	got := parseIPMISensors(raw)
	if len(got) != 2 {
		t.Fatalf("got %d sensors, want 2 (noise line should be skipped)", len(got))
	}
	if got[0].Name != "CPU1 Temp" || got[1].Name != "FAN1" {
		t.Errorf("unexpected sensor names: %+v", got)
	}
}

func TestParseIPMISensors_EmptyInput(t *testing.T) {
	if got := parseIPMISensors(""); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
