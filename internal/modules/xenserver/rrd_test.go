package xenserver

import "testing"

const sampleXPort = `<xport>
  <meta>
    <start>1720000000</start>
    <step>60</step>
    <end>1720000060</end>
    <rows>2</rows>
    <columns>3</columns>
    <legend>
      <entry>AVERAGE:host:host-uuid-1:cpu_avg</entry>
      <entry>AVERAGE:vm:vm-uuid-1:cpu0</entry>
      <entry>AVERAGE:vm:vm-uuid-1:memory</entry>
    </legend>
  </meta>
  <data>
    <row><t>1720000060</t><v>12.5</v><v>3.2</v><v>1073741824</v></row>
    <row><t>1720000000</t><v>10.0</v><v>2.0</v><v>1073741824</v></row>
  </data>
</xport>`

func TestParseRRDXPort_TakesNewestRow(t *testing.T) {
	snap, err := parseRRDXPort([]byte(sampleXPort))
	if err != nil {
		t.Fatalf("parseRRDXPort: %v", err)
	}
	if snap.EndTime != 1720000060 {
		t.Errorf("EndTime = %d, want 1720000060", snap.EndTime)
	}
	if snap.Host == nil {
		t.Fatal("Host is nil")
	}
	if snap.Host.UUID != "host-uuid-1" {
		t.Errorf("Host.UUID = %q", snap.Host.UUID)
	}
	if got := snap.Host.Metrics["cpu_avg"]; got != 12.5 {
		t.Errorf("Host cpu_avg = %v, want 12.5 (should use the newest/first row)", got)
	}

	if len(snap.VMs) != 1 {
		t.Fatalf("got %d VMs, want 1", len(snap.VMs))
	}
	vm := snap.VMs[0]
	if vm.UUID != "vm-uuid-1" {
		t.Errorf("VM UUID = %q", vm.UUID)
	}
	if vm.Metrics["cpu0"] != 3.2 {
		t.Errorf("VM cpu0 = %v, want 3.2", vm.Metrics["cpu0"])
	}
	if vm.Metrics["memory"] != 1073741824 {
		t.Errorf("VM memory = %v, want 1073741824", vm.Metrics["memory"])
	}
}

func TestParseRRDXPort_MultipleVMs(t *testing.T) {
	xportDoc := `<xport>
  <meta>
    <start>0</start><step>60</step><end>100</end><rows>1</rows><columns>2</columns>
    <legend>
      <entry>AVERAGE:vm:vm-a:cpu0</entry>
      <entry>AVERAGE:vm:vm-b:cpu0</entry>
    </legend>
  </meta>
  <data>
    <row><t>100</t><v>1.0</v><v>2.0</v></row>
  </data>
</xport>`
	snap, err := parseRRDXPort([]byte(xportDoc))
	if err != nil {
		t.Fatalf("parseRRDXPort: %v", err)
	}
	if len(snap.VMs) != 2 {
		t.Fatalf("got %d VMs, want 2", len(snap.VMs))
	}
	byUUID := map[string]float64{}
	for _, vm := range snap.VMs {
		byUUID[vm.UUID] = vm.Metrics["cpu0"]
	}
	if byUUID["vm-a"] != 1.0 || byUUID["vm-b"] != 2.0 {
		t.Errorf("unexpected per-VM values: %+v", byUUID)
	}
}

func TestParseRRDXPort_NoRows(t *testing.T) {
	xportDoc := `<xport>
  <meta><start>0</start><step>60</step><end>42</end><rows>0</rows><columns>0</columns><legend></legend></meta>
  <data></data>
</xport>`
	snap, err := parseRRDXPort([]byte(xportDoc))
	if err != nil {
		t.Fatalf("parseRRDXPort: %v", err)
	}
	if snap.Host != nil || len(snap.VMs) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap)
	}
	if snap.EndTime != 42 {
		t.Errorf("EndTime = %d, want 42", snap.EndTime)
	}
}

func TestParseRRDXPort_MalformedXML(t *testing.T) {
	if _, err := parseRRDXPort([]byte("not xml at all")); err == nil {
		t.Error("expected error for malformed XML")
	}
}
