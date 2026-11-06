package xenserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// VMFilter selects VMs for vm.list. Pointer fields distinguish "not given"
// from false. With no filter and no Fields, vm.list keeps its original
// behaviour (normal VMs only, fixed summary fields).
type VMFilter struct {
	NameLabel       string
	IsControlDomain *bool
	IsATemplate     *bool
	Fields          []string
}

// Empty reports whether no filter or field list was given.
func (f VMFilter) Empty() bool {
	return f.NameLabel == "" && f.IsControlDomain == nil && f.IsATemplate == nil && len(f.Fields) == 0
}

// VMListFiltered lists VMs with xe-side filters. With Fields (xe parameter
// names such as "uuid", "name-label", "power-state") it returns one object per
// VM holding exactly those fields; otherwise typed VMInfo records. Unlike
// vm.list's default, explicit filters are honoured as given, so
// is_a_template=true lists templates and is_control_domain=true lists dom0.
func (m *Module) VMListFiltered(ctx context.Context, f VMFilter) (any, error) {
	for _, fld := range f.Fields {
		if !paramNameRe.MatchString(fld) {
			return nil, fmt.Errorf("field %q must match [A-Za-z0-9_-]+", fld)
		}
	}
	// xe vm-list never shows templates; template-list does. So is_a_template=true
	// switches to template-list (control-domain filtering does not apply to templates).
	args := []string{"vm-list"}
	if f.IsATemplate != nil && *f.IsATemplate {
		args = []string{"template-list"}
	}
	if f.NameLabel != "" {
		args = append(args, "name-label="+f.NameLabel)
	}
	if f.IsControlDomain != nil && args[0] == "vm-list" {
		args = append(args, "is-control-domain="+strconv.FormatBool(*f.IsControlDomain))
	}
	if f.IsATemplate != nil && !*f.IsATemplate {
		args = append(args, "is-a-template=false")
	}
	if len(f.Fields) > 0 {
		args = append(args, "params="+strings.Join(f.Fields, ","))
		out, err := m.run(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("listing VMs: %w", err)
		}
		items := make([]map[string]string, 0)
		for _, b := range parseXEBlocks(out) {
			items = append(items, b)
		}
		return items, nil
	}
	args = append(args, "params=uuid,name-label,power-state,resident-on,VCPUs-number,memory-actual")
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("listing VMs: %w", err)
	}
	vms := make([]VMInfo, 0)
	for _, b := range parseXEBlocks(out) {
		vcpus, _ := strconv.Atoi(b["VCPUs-number"])
		mem, _ := strconv.ParseUint(b["memory-actual"], 10, 64)
		vms = append(vms, VMInfo{UUID: b["uuid"], NameLabel: b["name-label"], PowerState: b["power-state"],
			ResidentOn: b["resident-on"], VCPUs: vcpus, MemoryBytes: mem})
	}
	return vms, nil
}

// SRListFiltered lists SRs filtered by type (e.g. nfs, iso), name-label and
// content-type (e.g. iso, user).
func (m *Module) SRListFiltered(ctx context.Context, srType, nameLabel, contentType string) ([]SRInfo, error) {
	args := []string{"sr-list"}
	if srType != "" {
		args = append(args, "type="+srType)
	}
	if nameLabel != "" {
		args = append(args, "name-label="+nameLabel)
	}
	if contentType != "" {
		args = append(args, "content-type="+contentType)
	}
	args = append(args, "params=uuid,name-label,type,physical-size,physical-utilisation")
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("listing SRs: %w", err)
	}
	srs := make([]SRInfo, 0)
	for _, b := range parseXEBlocks(out) {
		srs = append(srs, srInfoFromBlock(b))
	}
	return srs, nil
}

// NetworkListFiltered lists networks filtered by name-label, bridge and/or the
// uuid of one of their interfaces.
func (m *Module) NetworkListFiltered(ctx context.Context, nameLabel, bridge, pifUUID string) ([]NetworkInfo, error) {
	args := []string{"network-list"}
	if nameLabel != "" {
		args = append(args, "name-label="+nameLabel)
	}
	if bridge != "" {
		args = append(args, "bridge="+bridge)
	}
	if pifUUID != "" {
		args = append(args, "PIF-uuids="+pifUUID)
	}
	args = append(args, "params=uuid,name-label,bridge")
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("listing networks: %w", err)
	}
	nets := make([]NetworkInfo, 0)
	for _, b := range parseXEBlocks(out) {
		nets = append(nets, NetworkInfo{UUID: b["uuid"], NameLabel: b["name-label"], Bridge: b["bridge"]})
	}
	return nets, nil
}

// SnapshotPromote turns a snapshot into an ordinary VM in place
// (snapshot-param-set is-a-template=false). newName, if set, renames it. The
// uuid stays the same. Use xenserver.vm.snapshot_list to find snapshots.
func (m *Module) SnapshotPromote(ctx context.Context, uuid, newName string) (*ActionResult, error) {
	if err := m.requireUUID("snapshot", uuid); err != nil {
		return nil, err
	}
	if out, err := m.run(ctx, "snapshot-list", "uuid="+uuid, "--minimal"); err != nil || strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("%s is not a snapshot", uuid)
	}
	if _, err := m.run(ctx, "snapshot-param-set", "uuid="+uuid, "is-a-template=false"); err != nil {
		return m.failure("snapshot-promote", uuid, err), nil
	}
	if newName != "" {
		if _, err := m.run(ctx, "vm-param-set", "uuid="+uuid, "name-label="+newName); err != nil {
			r := m.failure("snapshot-promote", uuid, err)
			r.Message = "promoted to a VM but renaming failed: " + r.Message
			return r, nil
		}
	}
	return &ActionResult{Operation: "snapshot-promote", Target: uuid, Success: true,
		Message: "Snapshot promoted to a VM; it can now be started.", Output: uuid,
		Data: map[string]string{"uuid": uuid}}, nil
}
