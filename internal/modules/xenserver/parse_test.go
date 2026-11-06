package xenserver

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseXEBlocks_MultipleRecords(t *testing.T) {
	out := `uuid ( RO)                    : 6b28ee71-3d15-4e3e-9d76-1234567890ab
         name-label ( RW): my-vm
         power-state ( RO): running


uuid ( RO)                    : 7c39ff82-4e26-5f4f-ae87-2345678901bc
         name-label ( RW): other-vm
         power-state ( RO): halted
`
	blocks := parseXEBlocks(out)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0]["uuid"] != "6b28ee71-3d15-4e3e-9d76-1234567890ab" {
		t.Errorf("block 0 uuid: got %q", blocks[0]["uuid"])
	}
	if blocks[0]["name-label"] != "my-vm" {
		t.Errorf("block 0 name-label: got %q", blocks[0]["name-label"])
	}
	if blocks[1]["power-state"] != "halted" {
		t.Errorf("block 1 power-state: got %q", blocks[1]["power-state"])
	}
}

func TestParseXEBlocks_SingleRecord(t *testing.T) {
	out := `uuid ( RO): abc-123
name-label ( RW): solo
`
	blocks := parseXEBlocks(out)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0]["uuid"] != "abc-123" {
		t.Errorf("uuid: got %q", blocks[0]["uuid"])
	}
}

func TestParseXEBlocks_EmptyOutput(t *testing.T) {
	if blocks := parseXEBlocks(""); len(blocks) != 0 {
		t.Errorf("got %d blocks, want 0", len(blocks))
	}
	if blocks := parseXEBlocks("\n\n\n"); len(blocks) != 0 {
		t.Errorf("got %d blocks for blank-only input, want 0", len(blocks))
	}
}

func TestParseXEBlocks_ValueContainingColons(t *testing.T) {
	out := `uuid ( RO): abc-123
address ( RW): fe80::1
time ( RO): 12:30:00`
	blocks := parseXEBlocks(out)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if got := blocks[0]["address"]; got != "fe80::1" {
		t.Errorf("address: got %q, want fe80::1", got)
	}
	if got := blocks[0]["time"]; got != "12:30:00" {
		t.Errorf("time: got %q, want 12:30:00", got)
	}
}

func TestPickUUID(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		want    string
	}{
		{"single match", "abc-123", false, "abc-123"},
		{"single match trimmed", "  abc-123  ", false, "abc-123"},
		{"no match", "", true, ""},
		{"whitespace only", "   ", true, ""},
		{"ambiguous", "abc-123,def-456", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickUUID(tt.raw, "name-label \"x\"")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got uuid %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitMinimalList(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b , c", []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		if got := splitMinimalList(tt.raw); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitMinimalList(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestSplitSetField(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"10.0.0.1", []string{"10.0.0.1"}},
		{"10.0.0.1; 10.0.0.2", []string{"10.0.0.1", "10.0.0.2"}},
	}
	for _, tt := range tests {
		if got := splitSetField(tt.raw); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitSetField(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestVBDToDisk(t *testing.T) {
	d := vbdToDisk(map[string]string{
		"uuid": "v1", "userdevice": "0", "type": "Disk", "mode": "RW",
		"bootable": "true", "currently-attached": "false", "vdi-uuid": "d1",
	})
	if d.Position != "0" || d.VDIUUID != "d1" || !d.Bootable || d.CurrentlyAttached || d.Mode != "RW" {
		t.Fatalf("unexpected: %+v", d)
	}
	cd := vbdToDisk(map[string]string{"userdevice": "3", "type": "CD", "vdi-uuid": "<not in database>"})
	if cd.VDIUUID != "" || cd.Type != "CD" {
		t.Fatalf("empty CD drive must have no VDI: %+v", cd)
	}
}

func TestNewXEErrorParsesDetail(t *testing.T) {
	stderr := "Error code: SR_BACKEND_FAILURE_88\nError parameters: , NFS SR creation error [opterr=13],\n"
	e := newXEError("/usr/bin/xe", []string{"sr-create", "device-config:cifspassword=hunter2", "name-label=x"},
		"", stderr, errors.New("command failed (exit 1): exit status 1"))
	if e.ExitCode != 1 || e.ErrorCode != "SR_BACKEND_FAILURE_88" {
		t.Fatalf("got %+v", e)
	}
	if len(e.ErrorArgs) != 1 || !strings.Contains(e.ErrorArgs[0], "opterr=13") {
		t.Fatalf("args: %q", e.ErrorArgs)
	}
	if strings.Contains(e.Command, "hunter2") || !strings.Contains(e.Command, "cifspassword=***") {
		t.Fatalf("secret not redacted: %s", e.Command)
	}
	if !strings.Contains(e.Error(), "Error code: SR_BACKEND_FAILURE_88") {
		t.Fatalf("Error() lost stderr: %s", e.Error())
	}
}

func TestFriendlyVMPowerError(t *testing.T) {
	badState := "You attempted an operation on a VM that was not in an appropriate power state at the time; for example, you attempted to start a VM that was already running. The parameters returned are the VM's handle, and the expected and actual VM state at the time of the call.\nvm: u (FortiGate)\nexpected: running\nactual: halted"
	lacks := "You attempted an operation on a VM which lacks the feature.\nvm: u (FortiGate)"
	noHosts := "There were no servers available to complete the specified operation."
	noHostsOut := "There are no suitable hosts to start this VM on.\n\nhost1    : Cannot start here [Host cannot attach to network: 98e0 (eth0)]"
	cases := []struct {
		name, op, state string
		force           bool
		xe              *XEError
		want            []string
	}{
		{"reboot halted", "vm-reboot", "halted", false, &XEError{Stderr: badState}, []string{`Cannot reboot VM "vm"`, "it is halted", "must be running", "Start it first"}},
		{"start running", "vm-start", "running", false, &XEError{Stderr: strings.Replace(badState, "expected: running\nactual: halted", "expected: halted\nactual: running", 1)}, []string{"it is running", "already running"}},
		{"resume halted", "vm-resume", "halted", false, &XEError{Stderr: strings.Replace(badState, "expected: running", "expected: suspended", 1)}, []string{"must be suspended", "use start"}},
		{"force on halted", "vm-shutdown", "halted", true, &XEError{Stderr: strings.Replace(badState, "expected: running", "expected: paused, running", 1)}, []string{"must be paused or running", "Start it first"}},
		{"shutdown no tools", "vm-shutdown", "running", false, &XEError{Stderr: lacks}, []string{"cleanly", "guest tools", "not installed or have not started", "xenserver.vm.force_shutdown"}},
		{"shutdown no tools forced", "vm-shutdown", "running", true, &XEError{Stderr: lacks}, []string{"Install the guest tools"}},
		{"start no hosts", "vm-start", "halted", false, &XEError{Stderr: noHosts, Stdout: noHostsOut}, []string{"no host can run it", "Host cannot attach to network"}},
		{"silent clean reboot", "vm-reboot", "running", false, &XEError{ExitCode: 1}, []string{"exited with code 1", "no error text", "xenserver.vm.force_reboot"}},
		{"timeout", "vm-reboot", "running", false, &XEError{cause: errors.New("command timed out after 10m0s: /usr/bin/xe")}, []string{"gave up waiting", "timed out after 10m0s", "check its state"}},
		{"template not a VM", "vm-start", "", false, &XEError{Stderr: "Error: No matching VMs found"}, []string{"no such VM", "xenserver.vm.promote_template"}},
		{"unknown", "vm-suspend", "running", false, &XEError{Stderr: "boom"}, []string{`Failed to suspend VM "vm"`, "boom"}},
	}
	for _, c := range cases {
		got := friendlyVMPowerError(c.op, "vm", c.state, c.force, c.xe)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing %q", c.name, got, w)
			}
		}
	}
}

func TestSnapshotExportValidatesArgs(t *testing.T) {
	m := &Module{}
	dir := t.TempDir()
	existing := dir + "/exists.xva"
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]string{
		"empty":       "",
		"relative":    "backup.xva",
		"missing dir": dir + "/nope/backup.xva",
		"overwrite":   existing,
	} {
		if _, err := m.SnapshotExport(context.Background(), "uuid", "", fn); err == nil {
			t.Errorf("%s: expected error for filename %q", name, fn)
		}
	}
}

func TestPromoteTemplateRequiresIdentifier(t *testing.T) {
	m := &Module{}
	if _, err := m.PromoteTemplate(context.Background(), "", "", ""); err == nil {
		t.Fatal("expected error when neither uuid nor name is given")
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText("<html><body><h1>HTTP 500 internal server error</h1>An unexpected error occurred; <h1> Additional information </h1>Db_exn.Read_missing_uuid(&quot;VDI&quot;)</body></html>")
	if strings.Contains(got, "<") || !strings.Contains(got, `HTTP 500 internal server error`) || !strings.Contains(got, `Read_missing_uuid("VDI")`) {
		t.Fatalf("got %q", got)
	}
}

func TestCheckExportTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/there.xva", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "rel.xva", dir + "/nodir/x.xva", dir + "/there.xva"} {
		if checkExportTarget(bad) == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	if err := checkExportTarget(dir + "/new.xva"); err != nil {
		t.Errorf("valid target rejected: %v", err)
	}
}

func TestFriendlyExportOfRunningVM(t *testing.T) {
	xe := &XEError{Stderr: "You attempted an operation on a VM that was not in an appropriate power state at the time\nexpected: halted, suspended\nactual: running"}
	got := friendlyVMPowerError("vm-export", "vm", "running", false, xe)
	for _, w := range []string{`Cannot export VM "vm"`, "halted or suspended", "snapshot_export"} {
		if !strings.Contains(got, w) {
			t.Errorf("%q missing %q", got, w)
		}
	}
}

func TestValidateParamKeyAndValue(t *testing.T) {
	good := []string{"name-label", "name-description", "VCPUs-max", "VCPUs-at-startup", "platform:cores-per-socket",
		"xenstore-data:vm-data/hostname", "other-config:x.y_z-1"}
	for _, k := range good {
		if err := validateParamKey(k); err != nil {
			t.Errorf("%q should be allowed: %v", k, err)
		}
	}
	bad := []string{"is-control-domain", "power-state", "memory-static-max", "HVM-boot-policy", "uuid",
		"bogus:key", "xenstore-data:", "xenstore-data:a b", "xenstore-data:a=b", "xenstore-data:a;b", ":x", ""}
	for _, k := range bad {
		if validateParamKey(k) == nil {
			t.Errorf("%q must be rejected", k)
		}
	}
	for _, c := range []struct{ k, v string }{{"VCPUs-max", "0"}, {"VCPUs-max", "x"}, {"VCPUs-at-startup", "-2"},
		{"name-label", "  "}, {"xenstore-data:k", "a\x00b"}, {"xenstore-data:k", strings.Repeat("a", maxParamValueLen+1)}} {
		if validateParamValue(c.k, c.v) == nil {
			t.Errorf("value %q for %s must be rejected", c.v, c.k)
		}
	}
	if err := validateParamValue("xenstore-data:k", `has "quotes", spaces; = and 'apostrophes'`); err != nil {
		t.Errorf("odd but legal value rejected: %v", err)
	}
}

func TestOrderedParamKeys(t *testing.T) {
	got := orderedParamKeys(map[string]string{"VCPUs-at-startup": "2", "name-label": "x", "VCPUs-max": "4", "a:b": "1"})
	want := []string{"VCPUs-max", "VCPUs-at-startup", "a:b", "name-label"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseParamList(t *testing.T) {
	out := `uuid ( RO)                    : 261c8051-0678-5d1a-fb9a-df61ca51d6b6
          name-label ( RW): web: one
    name-description ( RW): 
          power-state ( RO): halted
 allowed-operations (SRO): start; clone; destroy
         xenstore-data (MRW): vm-data/hostname: web1; vm-data/note: a: b
             platform (MRW): cores-per-socket: 2; viridian: true
          other-config (MRW): 
                 VBDs (SRO): 
`
	p, m, s := parseParamList(out)
	if p["name-label"] != "web: one" || p["power-state"] != "halted" || p["name-description"] != "" {
		t.Errorf("scalars: %v", p)
	}
	if !reflect.DeepEqual(s["allowed-operations"], []string{"start", "clone", "destroy"}) || len(s["VBDs"]) != 0 {
		t.Errorf("sets: %v", s)
	}
	if m["xenstore-data"]["vm-data/hostname"] != "web1" || m["xenstore-data"]["vm-data/note"] != "a: b" ||
		m["platform"]["cores-per-socket"] != "2" {
		t.Errorf("maps: %v", m)
	}
	if mm, ok := m["other-config"]; !ok || len(mm) != 0 {
		t.Errorf("empty map must be present and empty: %v", m["other-config"])
	}
	if _, leaked := p["xenstore-data"]; leaked {
		t.Error("map field leaked into scalars")
	}
}

func TestFriendlyDeviceError(t *testing.T) {
	power := &XEError{Stderr: "You attempted an operation on a VM that was not in an appropriate power state at the time\nexpected: halted\nactual: running"}
	cases := []struct {
		name string
		xe   *XEError
		want []string
	}{
		{"power", power, []string{`Cannot detach disk from "vm"`, "the VM is running", "must be halted"}},
		{"in use", &XEError{Stderr: "Error code: VDI_IN_USE\nError parameters: OpaqueRef:x, destroy"}, []string{"disk is in use", "detach it"}},
		{"bad uuid", &XEError{Stderr: "The uuid you supplied was invalid."}, []string{"does not exist"}},
		{"position taken", &XEError{Stderr: "Error code: DEVICE_ALREADY_ATTACHED"}, []string{"already in use", "another position"}},
		{"other", &XEError{Stderr: "boom"}, []string{"Failed to detach disk from", "boom"}},
		{"no PV drivers", &XEError{Stderr: "You attempted an operation on a VM which requires PV drivers to be installed but the drivers were not detected."}, []string{"no Xen PV drivers", "stop the VM first"}},
	}
	for _, c := range cases {
		got := friendlyDeviceError("detach disk from", "vm", "running", c.xe)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing %q", c.name, got, w)
			}
		}
	}
}

func TestRedactSecretsInParamLists(t *testing.T) {
	params := map[string]string{"uuid": "u", "name-label": "x", "cifspassword": "hunter2", "other": ""}
	maps := map[string]map[string]string{
		"device-config": {"server": "10.0.0.9", "username": "bob", "cifspassword": "hunter2", "Secret": "s", "empty_password": ""},
		"other-config":  {"note": "fine"},
	}
	redactSecrets(params, maps)
	if params["cifspassword"] != "***" || params["name-label"] != "x" || params["other"] != "" {
		t.Errorf("scalars: %v", params)
	}
	dc := maps["device-config"]
	if dc["cifspassword"] != "***" || dc["Secret"] != "***" || dc["server"] != "10.0.0.9" || dc["username"] != "bob" || dc["empty_password"] != "" {
		t.Errorf("device-config: %v", dc)
	}
	if maskedValue("device-config", "cifspassword", "x") != "***" || maskedValue("name-label", "", "web") != "web" {
		t.Error("maskedValue wrong")
	}
}

func TestCommandLineMasksXenstoreData(t *testing.T) {
	line := commandLine("/usr/bin/xe", []string{"vm-param-set", "uuid=u", "xenstore-data:vm-data/user-data=#cloud-config password: x", "name-label=web"})
	if strings.Contains(line, "cloud-config") || strings.Contains(line, "password: x") {
		t.Fatalf("xenstore-data value leaked into the command line: %s", line)
	}
	if !strings.Contains(line, "xenstore-data:vm-data/user-data=***") || !strings.Contains(line, "name-label=web") {
		t.Fatalf("unexpected command line: %s", line)
	}
}

func TestParseMapValueKeepsSemicolonsInsideValues(t *testing.T) {
	got := parseMapValue(`a: 1; vm-data/test: web "one" it's; a=b  c; z.k/1: last`)
	want := map[string]string{"a": "1", "vm-data/test": `web "one" it's; a=b  c`, "z.k/1": "last"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if m := parseMapValue(""); len(m) != 0 {
		t.Fatalf("empty: %v", m)
	}
	if m := parseMapValue("lone:"); m["lone"] != "" || len(m) != 1 {
		t.Fatalf("lone: %v", m)
	}
}

func TestFriendlyPlugAlreadyAttached(t *testing.T) {
	xe := &XEError{Stderr: "Error code: DEVICE_ALREADY_ATTACHED"}
	if got := friendlyDeviceError("plug disk", "vm", "running", xe); !strings.Contains(got, "already plugged in") {
		t.Errorf("plug of a plugged disk: %q", got)
	}
	if got := friendlyDeviceError("attach disk to", "vm", "halted", xe); !strings.Contains(got, "position is already in use") {
		t.Errorf("create at a taken position must keep the position message: %q", got)
	}
	if got := friendlyDeviceError("unplug disk", "vm", "running", xe); strings.Contains(got, "already plugged in") {
		t.Errorf("unplug must not claim already plugged in: %q", got)
	}
}

func TestFilterIPMIAndRRD(t *testing.T) {
	s := []IPMISensor{{Name: "Temp", Units: "degrees C"}, {Name: "Fan1", Units: "RPM"}, {Name: "PG", Units: "discrete"}}
	if got := FilterIPMI(s, []string{"rpm", " Degrees C "}); len(got) != 2 || got[0].Name != "Temp" || got[1].Name != "Fan1" {
		t.Errorf("units filter: %v", got)
	}
	if got := FilterIPMI(s, nil); len(got) != 3 {
		t.Errorf("no filter keeps all: %v", got)
	}
	snap := &RRDSnapshot{Host: &HostMetric{UUID: "h"}, EndTime: 7, VMs: []VMMetric{{UUID: "a"}, {UUID: "b"}}}
	if got := FilterRRD(snap, "b"); got.Host != nil || len(got.VMs) != 1 || got.VMs[0].UUID != "b" || got.EndTime != 7 {
		t.Errorf("vm filter: %+v", got)
	}
	if got := FilterRRD(snap, ""); got != snap {
		t.Error("empty uuid must return the snapshot unchanged")
	}
}
