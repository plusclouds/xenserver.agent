package dispatcher

import (
	"strings"
	"testing"
)

func TestRedactParams(t *testing.T) {
	raw := []byte(`{"uuid":"vm-1","set":{"xenstore-data:vm-data/cloud-init":"#cloud-config\npassword: hunter2","name-label":"web","XENSTORE-DATA:x":"s"},` +
		`"device_config":{"server":"10.0.0.9","cifspassword":"hunter2"},"api_token":"abc","list":[{"secret_key":"zzz","ok":"fine"}]}`)
	got := string(redactParams(raw))
	for _, leak := range []string{"hunter2", "cloud-config", "abc", "zzz", `"s"`} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in %s", leak, got)
		}
	}
	for _, keep := range []string{"vm-1", "web", "10.0.0.9", "fine"} {
		if !strings.Contains(got, keep) {
			t.Errorf("over-redacted: %q missing in %s", keep, got)
		}
	}
	if string(redactParams([]byte("not json"))) != `"<unparseable params>"` {
		t.Error("non-JSON params must not be logged verbatim")
	}
}
