package executor

import (
	"reflect"
	"testing"
)

func TestRedactArgs(t *testing.T) {
	in := []string{"vm-param-set", "uuid=abc", "xenstore-data:vm-data/note=top secret; a=b", "XenStore-Data:x=y",
		"device-config:cifspassword=hunter2", "api-token=t", "name-label=web", "xenstore-data:empty=", "--minimal"}
	want := []string{"vm-param-set", "uuid=abc", "xenstore-data:vm-data/note=***", "XenStore-Data:x=***",
		"device-config:cifspassword=***", "api-token=***", "name-label=web", "xenstore-data:empty=", "--minimal"}
	got := RedactArgs(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if in[2] != "xenstore-data:vm-data/note=top secret; a=b" {
		t.Fatal("RedactArgs must not modify its input")
	}
}
