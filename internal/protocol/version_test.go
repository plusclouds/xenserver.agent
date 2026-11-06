package protocol

import "testing"

func TestNormalizeVersion(t *testing.T) {
	if v, err := NormalizeVersion(0); err != nil || v != 1 {
		t.Fatalf("omitted version: %d %v", v, err)
	}
	if v, err := NormalizeVersion(1); err != nil || v != 1 {
		t.Fatalf("v1: %d %v", v, err)
	}
	for _, bad := range []int{2, -1, 99} {
		if _, err := NormalizeVersion(bad); err == nil {
			t.Errorf("version %d accepted", bad)
		}
	}
}

func TestResultEchoesRequestVersion(t *testing.T) {
	res, err := ResultFor(Envelope{V: 0, ID: "x"}, "u", StatusCompleted, "", nil)
	if err != nil || res.V != 1 {
		t.Fatalf("result version %d err %v", res.V, err)
	}
}
