package openvswitch

import (
	"fmt"
	"strings"
)

// splitLines splits ovs-vsctl/ovs-ofctl line-oriented output into trimmed,
// non-empty lines.
func splitLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// validateFlowSpec rejects flow/match specs that could be mistaken for
// ovs-ofctl flags (a leading "-") or that smuggle extra argv-adjacent
// content via embedded whitespace — a single OVS flow entry is always one
// whitespace-free token.
func validateFlowSpec(spec string) error {
	if spec == "" {
		return fmt.Errorf("flow spec is required")
	}
	if strings.HasPrefix(spec, "-") {
		return fmt.Errorf("flow spec must not start with '-'")
	}
	if strings.ContainsAny(spec, " \t\n\r") {
		return fmt.Errorf("flow spec must not contain whitespace")
	}
	return nil
}
