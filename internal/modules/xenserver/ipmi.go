package xenserver

import (
	"context"
	"fmt"
	"strings"
)

// IPMISensor is one row of `ipmitool sensor` output: a named sensor with its
// current reading and, where reported, its threshold bounds. Threshold
// fields (LNR..UNR) are left empty rather than omitted when ipmitool doesn't
// report them for a given sensor (e.g. discrete/state sensors).
type IPMISensor struct {
	Name    string `json:"name"`
	Reading string `json:"reading"`
	Units   string `json:"units"`
	Status  string `json:"status"`
	// LNR/LCR/LNC are the lower non-recoverable/critical/non-critical
	// thresholds; UNC/UCR/UNR are the upper non-critical/critical/
	// non-recoverable thresholds.
	LNR string `json:"lnr,omitempty"`
	LCR string `json:"lcr,omitempty"`
	LNC string `json:"lnc,omitempty"`
	UNC string `json:"unc,omitempty"`
	UCR string `json:"ucr,omitempty"`
	UNR string `json:"unr,omitempty"`
}

// IPMISensors runs `ipmitool sensor` and returns the parsed sensor table.
// Replaces the ipmi.py sidecar script.
func (m *Module) IPMISensors(ctx context.Context) ([]IPMISensor, error) {
	stdout, stderr, err := m.exec.Execute(ctx, m.ipmiBinary, "sensor")
	if err != nil {
		if s := strings.TrimSpace(stderr); s != "" {
			return nil, fmt.Errorf("%w: %s", err, s)
		}
		return nil, err
	}
	return parseIPMISensors(stdout), nil
}

// parseIPMISensors parses ipmitool's "|"-delimited sensor table. Each line
// has the shape:
//
//	Sensor Name | Reading | Units | Status | LNR | LCR | LNC | UNC | UCR | UNR
//
// Trailing threshold columns are frequently "na" for discrete sensors, and
// some sensors report fewer columns entirely — only the first three
// (name/reading/units) are required; everything past that is best-effort.
func parseIPMISensors(raw string) []IPMISensor {
	var sensors []IPMISensor
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 3 {
			continue
		}

		s := IPMISensor{Name: parts[0], Reading: parts[1], Units: parts[2]}
		fields := []*string{&s.Status, &s.LNR, &s.LCR, &s.LNC, &s.UNC, &s.UCR, &s.UNR}
		for i, f := range fields {
			if idx := 3 + i; idx < len(parts) {
				*f = parts[idx]
			}
		}
		sensors = append(sensors, s)
	}
	return sensors
}

// FilterIPMI keeps only sensors whose units match one of units
// (case-insensitive, e.g. "degrees C", "RPM", "Watts", "Volts", "Amps",
// "percent", "discrete"). Empty units keeps everything.
func FilterIPMI(sensors []IPMISensor, units []string) []IPMISensor {
	if len(units) == 0 {
		return sensors
	}
	out := make([]IPMISensor, 0, len(sensors))
	for _, s := range sensors {
		for _, u := range units {
			if strings.EqualFold(strings.TrimSpace(u), s.Units) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}
