package openvswitch

import (
	"reflect"
	"testing"
)

func TestSplitLines(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"\n\n", nil},
		{"br0", []string{"br0"}},
		{"br0\nbr1\n", []string{"br0", "br1"}},
		{"  br0  \n\n  br1  ", []string{"br0", "br1"}},
	}
	for _, tt := range tests {
		if got := splitLines(tt.raw); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitLines(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestValidateFlowSpec(t *testing.T) {
	valid := []string{
		"priority=100,ip,nw_src=10.0.0.0/24,actions=drop",
		"in_port=1,actions=output:2",
	}
	for _, spec := range valid {
		if err := validateFlowSpec(spec); err != nil {
			t.Errorf("validateFlowSpec(%q) unexpected error: %v", spec, err)
		}
	}

	invalid := []string{
		"",
		"-not-a-flow",
		"--bundle",
		"priority=100 actions=drop",
		"priority=100\tactions=drop",
	}
	for _, spec := range invalid {
		if err := validateFlowSpec(spec); err == nil {
			t.Errorf("validateFlowSpec(%q) expected error, got nil", spec)
		}
	}
}
