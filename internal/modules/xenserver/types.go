// Package xenserver manages a XenServer/XCP-ng hypervisor host via the xe CLI
// and, for raw disk export, the local xapi HTTP interface. It is only
// functional when the agent runs as root on Dom0.
package xenserver

// PoolInfo describes the resource pool this host belongs to.
type PoolInfo struct {
	UUID      string `json:"uuid"`
	NameLabel string `json:"name_label"`
	// Master is the uuid of the pool's master host.
	Master    string `json:"master"`
	HAEnabled bool   `json:"ha_enabled"`
}

// HostSummary is a single row from the pool's host list.
type HostSummary struct {
	UUID      string `json:"uuid"`
	NameLabel string `json:"name_label"`
	Hostname  string `json:"hostname"`
	Address   string `json:"address"`
	Enabled   bool   `json:"enabled"`
}

// HostInfo is the detailed record for a single host.
type HostInfo struct {
	UUID      string `json:"uuid"`
	NameLabel string `json:"name_label"`
	Hostname  string `json:"hostname"`
	Address   string `json:"address"`
	Enabled   bool   `json:"enabled"`
	// SoftwareVersion is xe's raw nested software-version blob, kept as a
	// single string rather than decomposed into its many sub-keys.
	SoftwareVersion string `json:"software_version"`
}

// VMInfo is a single row from the VM list. Control domains and templates are
// excluded by VMList — they are not customer-facing VMs.
type VMInfo struct {
	UUID        string `json:"uuid"`
	NameLabel   string `json:"name_label"`
	PowerState  string `json:"power_state"`
	ResidentOn  string `json:"resident_on"`
	VCPUs       int    `json:"vcpus"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

// VMDetail is the fuller record returned by VMGet.
type VMDetail struct {
	UUID        string `json:"uuid"`
	NameLabel   string `json:"name_label"`
	Description string `json:"description"`
	PowerState  string `json:"power_state"`
	ResidentOn  string `json:"resident_on"`
	VCPUs       int    `json:"vcpus"`
	MemoryBytes uint64 `json:"memory_bytes"`
	// OSVersion is xe's raw nested os-version blob, kept as a single string.
	OSVersion   string `json:"os_version"`
	IsATemplate bool   `json:"is_a_template"`
}

// SnapshotInfo describes a VM snapshot. Snapshots are VM-class objects in the
// XenAPI, so SnapshotInfo.UUID is the snapshot's own uuid, not the parent's.
type SnapshotInfo struct {
	UUID         string `json:"uuid"`
	NameLabel    string `json:"name_label"`
	SnapshotOf   string `json:"snapshot_of"`
	SnapshotTime string `json:"snapshot_time"`
}

// SRInfo describes a storage repository.
type SRInfo struct {
	UUID              string `json:"uuid"`
	NameLabel         string `json:"name_label"`
	Type              string `json:"type"`
	PhysicalSizeBytes uint64 `json:"physical_size_bytes"`
	PhysicalUtilBytes uint64 `json:"physical_utilisation_bytes"`
}

// NetworkInfo describes a xapi network and its underlying OVS bridge.
type NetworkInfo struct {
	UUID      string `json:"uuid"`
	NameLabel string `json:"name_label"`
	Bridge    string `json:"bridge"`
}

// VIFInfo describes a virtual network interface attached to a VM.
type VIFInfo struct {
	UUID        string   `json:"uuid"`
	Device      string   `json:"device"`
	MAC         string   `json:"mac"`
	NetworkUUID string   `json:"network_uuid"`
	LockingMode string   `json:"locking_mode"`
	IPv4Allowed []string `json:"ipv4_allowed,omitempty"`
	IPv6Allowed []string `json:"ipv6_allowed,omitempty"`
}

// VDIInfo describes a virtual disk image.
type VDIInfo struct {
	UUID              string `json:"uuid"`
	NameLabel         string `json:"name_label"`
	VirtualSizeBytes  uint64 `json:"virtual_size_bytes"`
	PhysicalUtilBytes uint64 `json:"physical_utilisation_bytes"`
	SRUUID            string `json:"sr_uuid"`
	Type              string `json:"type"`
}

// ActionResult is the outcome of a state-changing xenserver.* operation.
// A command failure is reported as Success:false with a nil Go error — the
// dispatcher reports "completed" with output.success=false, matching the
// convention already used by internal/modules/services.
type ActionResult struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	Output    string `json:"output,omitempty"`
	// Detail and Diagnostics are set only on failures. Detail is the failed xe
	// command's context; Diagnostics are agent-gathered explanations (e.g. why a
	// VM could not start).
	Detail *ErrorDetail `json:"detail,omitempty"`
	// Data is structured output for operations that return more than a string
	// (for example {"uuid": "<new vm>"} or the list of applied changes).
	Data        any      `json:"data,omitempty"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// VMDiskInfo describes one virtual block device (VBD) of a VM together with
// the disk image (VDI) it exposes. Position is the VBD userdevice — "0" is
// the first disk — and is what xenserver.vdi.resize's device parameter takes.
type VMDiskInfo struct {
	Position          string `json:"position"`
	VBDUUID           string `json:"vbd_uuid"`
	Type              string `json:"type"` // Disk or CD
	Mode              string `json:"mode"` // RW or RO
	Bootable          bool   `json:"bootable"`
	CurrentlyAttached bool   `json:"currently_attached"`
	VDIUUID           string `json:"vdi_uuid,omitempty"` // empty for an empty CD drive
	NameLabel         string `json:"name_label,omitempty"`
	VirtualSizeBytes  uint64 `json:"virtual_size_bytes,omitempty"`
	SRUUID            string `json:"sr_uuid,omitempty"`
}
