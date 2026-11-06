package xenserver

import (
	"context"
	"fmt"
	"strings"
)

// Inventory is a one-call snapshot of the host and its pool: every object is
// the full xe parameter list (xe-style keys, secrets masked), the same shape
// the individual *.param_list operations return.
type Inventory struct {
	Pool     *PoolInfo   `json:"pool"`
	Hosts    []ParamList `json:"hosts"`
	PIFs     []ParamList `json:"pifs"`
	Networks []ParamList `json:"networks"`
	VLANs    []ParamList `json:"vlans"`
	SRs      []ParamList `json:"srs"`
	PBDs     []ParamList `json:"pbds"`
	// VMs is present only when requested (guest VMs; control domains,
	// templates and snapshots are excluded).
	VMs []ParamList `json:"vms,omitempty"`
}

// classParamLists runs "xe <class>-list params=all [filters]", which prints
// every object of the class with all its parameters in the same annotated
// format as <class>-param-list (that command itself needs a uuid), and
// structures each block.
func (m *Module) classParamLists(ctx context.Context, class string, filters ...string) ([]ParamList, error) {
	out, err := m.runTimeout(ctx, timeoutMedium, append(append([]string{class + "-list"}, filters...), "params=all")...)
	if err != nil {
		return nil, fmt.Errorf("listing %s parameters: %w", class, err)
	}
	return splitParamListBlocks(out), nil
}

// splitParamListBlocks splits bulk "xe <class>-list params=all" output (objects
// separated by blank lines) into structured, secret-masked parameter lists.
func splitParamListBlocks(out string) []ParamList {
	lists := []ParamList{}
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		p, maps, sets := parseParamList(block)
		if p["uuid"] == "" {
			continue
		}
		redactSecrets(p, maps)
		lists = append(lists, ParamList{UUID: p["uuid"], Params: p, Maps: maps, Sets: sets})
	}
	return lists
}

// InventorySnapshot gathers pool, host, interface, network, VLAN, storage and
// attachment records in one call, so a caller syncing a host needs one
// request instead of one per object.
func (m *Module) InventorySnapshot(ctx context.Context, includeVMs bool) (*Inventory, error) {
	inv := &Inventory{}
	var err error
	if inv.Pool, err = m.PoolInfo(ctx); err != nil {
		return nil, err
	}
	classes := []struct {
		class string
		dst   *[]ParamList
	}{
		{"host", &inv.Hosts}, {"pif", &inv.PIFs}, {"network", &inv.Networks},
		{"vlan", &inv.VLANs}, {"sr", &inv.SRs}, {"pbd", &inv.PBDs},
	}
	for _, c := range classes {
		if *c.dst, err = m.classParamLists(ctx, c.class); err != nil {
			return nil, err
		}
	}
	if includeVMs {
		if inv.VMs, err = m.classParamLists(ctx, "vm", "is-control-domain=false", "is-a-template=false", "is-a-snapshot=false"); err != nil {
			return nil, err
		}
	}
	return inv, nil
}
