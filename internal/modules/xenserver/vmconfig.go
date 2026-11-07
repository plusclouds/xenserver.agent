package xenserver

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ParamList is an object's full parameter set (VM, VDI, VBD, VIF, ...), split
// by xe's own type markers: scalar fields, map fields ("MRW"/"MRO", e.g.
// xenstore-data, platform, other-config) as real JSON objects, and set fields
// ("SRW"/"SRO", e.g. allowed-operations, VBDs) as arrays.
type ParamList struct {
	UUID   string                       `json:"uuid"`
	Params map[string]string            `json:"params"`
	Maps   map[string]map[string]string `json:"maps"`
	Sets   map[string][]string          `json:"sets"`
}

// VMParamValue is the result of vm.param_get.
type VMParamValue struct {
	UUID  string `json:"uuid"`
	Param string `json:"param"`
	Key   string `json:"key,omitempty"`
	Value string `json:"value"`
}

// ConsoleInfo describes one VM console.
type ConsoleInfo struct {
	UUID     string `json:"uuid"`
	Protocol string `json:"protocol"`
	Location string `json:"location"`
}

// vm.param_set allowlist. Only these names may be written; anything else
// (is-control-domain, the VM's power state, ...) is rejected.
var (
	simpleVMParams = map[string]bool{"name-label": true, "name-description": true, "VCPUs-max": true, "VCPUs-at-startup": true}
	mapVMParams    = map[string]bool{"platform": true, "xenstore-data": true, "other-config": true}
	intVMParams    = map[string]bool{"VCPUs-max": true, "VCPUs-at-startup": true}
	mapKeyRe       = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`)
	paramNameRe    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	uuidRe         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const maxParamValueLen = 8192

// validateParamKey checks one vm.param_set key: a simple allowed name, or
// "<map>:<key>" for an allowed map field with a safe key.
func validateParamKey(key string) error {
	if simpleVMParams[key] {
		return nil
	}
	if i := strings.Index(key, ":"); i > 0 {
		name, mapKey := key[:i], key[i+1:]
		if !mapVMParams[name] {
			return fmt.Errorf("parameter map %q is not allowed (allowed: platform, xenstore-data, other-config)", name)
		}
		if !mapKeyRe.MatchString(mapKey) {
			return fmt.Errorf("map key %q in %q must match [A-Za-z0-9_.-/]+", mapKey, key)
		}
		return nil
	}
	return fmt.Errorf("parameter %q is not allowed (allowed: name-label, name-description, VCPUs-max, VCPUs-at-startup, platform:<key>, xenstore-data:<key>, other-config:<key>)", key)
}

func validateParamValue(key, value string) error {
	if len(value) > maxParamValueLen {
		return fmt.Errorf("value for %q is longer than %d bytes", key, maxParamValueLen)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("value for %q contains a NUL byte", key)
	}
	if intVMParams[key] {
		if n, err := strconv.Atoi(value); err != nil || n < 1 {
			return fmt.Errorf("%s must be a positive integer, got %q", key, value)
		}
	}
	if key == "name-label" && strings.TrimSpace(value) == "" {
		return fmt.Errorf("name-label must not be empty")
	}
	return nil
}

// orderedParamKeys sorts keys so VCPUs-max is applied before VCPUs-at-startup
// (raising both needs the maximum first); the rest are alphabetical.
func orderedParamKeys(set map[string]string) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		rank := func(k string) int {
			switch k {
			case "VCPUs-max":
				return 0
			case "VCPUs-at-startup":
				return 1
			}
			return 2
		}
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// VMParamSet writes allowlisted VM parameters. set maps parameter to value
// ("name-label", "xenstore-data:<key>", ...); unset lists map entries to
// remove ("xenstore-data:<key>"). Each value reaches xe as a single argument
// (no shell), so spaces and quotes in values are safe. Nothing is changed if
// any key or value fails validation; once applying starts, the first xe
// failure stops it and the result lists what was already applied.
func (m *Module) VMParamSet(ctx context.Context, uuid, name string, set map[string]string, unset []string) (*ActionResult, error) {
	if len(set) == 0 && len(unset) == 0 {
		return nil, fmt.Errorf("set (or unset) is required")
	}
	for k, v := range set {
		if err := validateParamKey(k); err != nil {
			return nil, err
		}
		if err := validateParamValue(k, v); err != nil {
			return nil, err
		}
	}
	for _, k := range unset {
		if i := strings.Index(k, ":"); i <= 0 || validateParamKey(k) != nil {
			return nil, fmt.Errorf("unset key %q must be a map entry such as xenstore-data:<key>", k)
		}
	}
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}

	var applied []string
	for _, k := range orderedParamKeys(set) {
		if _, err := m.run(ctx, "vm-param-set", "uuid="+resolved, k+"="+set[k]); err != nil {
			r := m.failure("vm-param-set", resolved, err)
			r.Message = fmt.Sprintf("setting %s failed after applying %d of %d changes: %s", k, len(applied), len(set)+len(unset), err)
			r.Data = map[string]any{"applied": applied, "failed": k}
			return r, nil
		}
		applied = append(applied, k)
	}
	for _, k := range unset {
		i := strings.Index(k, ":")
		if _, err := m.run(ctx, "vm-param-remove", "uuid="+resolved, "param-name="+k[:i], "param-key="+k[i+1:]); err != nil {
			r := m.failure("vm-param-remove", resolved, err)
			r.Message = fmt.Sprintf("removing %s failed after applying %d of %d changes: %s", k, len(applied), len(set)+len(unset), err)
			r.Data = map[string]any{"applied": applied, "failed": k}
			return r, nil
		}
		applied = append(applied, "-"+k)
	}
	return &ActionResult{Operation: "vm-param-set", Target: resolved, Success: true,
		Message: fmt.Sprintf("%d change(s) applied.", len(applied)),
		Data:    map[string]any{"applied": applied}}, nil
}

// VMParamGet reads one parameter (or one key of a map parameter).
func (m *Module) VMParamGet(ctx context.Context, uuid, name, param, key string) (*VMParamValue, error) {
	if !paramNameRe.MatchString(param) {
		return nil, fmt.Errorf("param must match [A-Za-z0-9_-]+ (for a map entry give param=xenstore-data, param_key=<key>)")
	}
	if key != "" && !mapKeyRe.MatchString(key) {
		return nil, fmt.Errorf("param_key %q must match [A-Za-z0-9_.-/]+", key)
	}
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	args := []string{"vm-param-get", "uuid=" + resolved, "param-name=" + param}
	if key != "" {
		args = append(args, "param-key="+key)
	}
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return &VMParamValue{UUID: resolved, Param: param, Key: key, Value: maskedValue(param, key, strings.TrimRight(out, "\r\n"))}, nil
}

// VMParamList returns every parameter of a VM, with map and set fields
// structured.
func (m *Module) VMParamList(ctx context.Context, uuid, name string) (*ParamList, error) {
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	pl, err := m.paramList(ctx, "vm", resolved)
	if err != nil {
		return nil, err
	}
	m.addVIFsSet(ctx, pl)
	return pl, nil
}

// addVIFsSet adds a "VIFs" set (xe's vm-param-list does not print one on this
// version) listing the VM's network card uuids, so callers get the same view
// they have for VBDs.
func (m *Module) addVIFsSet(ctx context.Context, pl *ParamList) {
	if _, ok := pl.Sets["VIFs"]; ok {
		return
	}
	out, err := m.run(ctx, "vif-list", "vm-uuid="+pl.UUID, "params=uuid", "--minimal")
	if err != nil {
		return
	}
	vifs := splitMinimalList(out)
	if vifs == nil {
		vifs = []string{}
	}
	pl.Sets["VIFs"] = vifs
}

// paramList runs "xe <class>-param-list" and structures the output.
func (m *Module) paramList(ctx context.Context, class, uuid string) (*ParamList, error) {
	out, err := m.run(ctx, class+"-param-list", "uuid="+uuid)
	if err != nil {
		return nil, err
	}
	p, maps, sets := parseParamList(out)
	redactSecrets(p, maps)
	return &ParamList{UUID: uuid, Params: p, Maps: maps, Sets: sets}, nil
}

// redactSecrets masks secret-looking entries (password, secret, passwd in the
// key) in every scalar and map field, so no parameter list leaks credentials
// such as an SR's device-config password. The same masking applies to
// pbd.list, vm/object param_get and every *.param_list.
func redactSecrets(params map[string]string, maps map[string]map[string]string) {
	for k := range params {
		if secretKey(k) && params[k] != "" {
			params[k] = "***"
		}
	}
	for _, mm := range maps {
		for k := range mm {
			if secretKey(k) && mm[k] != "" {
				mm[k] = "***"
			}
		}
	}
}

// maskedValue returns "***" when the requested parameter or map key looks secret.
func maskedValue(param, key, value string) string {
	if (secretKey(param) || (key != "" && secretKey(key))) && value != "" {
		return "***"
	}
	return value
}

// parseParamList parses "xe <class>-param-list" output. The annotation after
// each field name tells its type: "(MRW)"/"(MRO)" map, "(SRW)"/"(SRO)" set,
// "(RW)"/"(RO)" scalar.
func parseParamList(out string) (map[string]string, map[string]map[string]string, map[string][]string) {
	params := map[string]string{}
	maps := map[string]map[string]string{}
	sets := map[string][]string{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		idx := strings.Index(line, ":")
		if strings.TrimSpace(line) == "" || idx < 0 {
			continue
		}
		left := strings.TrimSpace(line[:idx])
		key, ann := splitAnnotation(left)
		if key == "" {
			continue
		}
		value := strings.TrimSpace(line[idx+1:])
		switch {
		case strings.HasPrefix(ann, "M"):
			maps[key] = parseMapValue(value)
		case strings.HasPrefix(ann, "S"):
			sets[key] = splitSetField(value)
		default:
			params[key] = value
		}
	}
	return params, maps, sets
}

// mapEntryStart matches the start of a new map entry: "<key>: ".
var mapEntryStart = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+: `)

// parseMapValue parses xe's "k1: v1; k2: v2" map rendering. A value may itself
// contain "; ", so a "; " only separates entries when what follows looks like
// a new "<key>: " (keys cannot contain spaces, "=" or ";"). Exact values are
// always available through param_get with param_key.
func parseMapValue(value string) map[string]string {
	out := map[string]string{}
	var entries []string
	cur := ""
	for i, part := range strings.Split(value, "; ") {
		if i > 0 && !mapEntryStart.MatchString(part) && cur != "" {
			cur += "; " + part
			continue
		}
		if cur != "" {
			entries = append(entries, cur)
		}
		cur = part
	}
	if cur != "" {
		entries = append(entries, cur)
	}
	for _, item := range entries {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if i := strings.Index(item, ": "); i >= 0 {
			out[item[:i]] = item[i+2:]
		} else {
			out[strings.TrimSuffix(item, ":")] = ""
		}
	}
	return out
}

// VMMemoryLimitsSet sets all four memory limits (bytes) at once. xapi
// requires static-min <= dynamic-min <= dynamic-max <= static-max and a halted VM.
func (m *Module) VMMemoryLimitsSet(ctx context.Context, uuid, name string, staticMin, staticMax, dynamicMin, dynamicMax int64) (*ActionResult, error) {
	if staticMin <= 0 || staticMax <= 0 || dynamicMin <= 0 || dynamicMax <= 0 {
		return nil, fmt.Errorf("static_min, static_max, dynamic_min and dynamic_max (bytes, all > 0) are required")
	}
	if !(staticMin <= dynamicMin && dynamicMin <= dynamicMax && dynamicMax <= staticMax) {
		return nil, fmt.Errorf("limits must satisfy static_min <= dynamic_min <= dynamic_max <= static_max")
	}
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	_, err = m.run(ctx, "vm-memory-limits-set", "uuid="+resolved,
		"static-min="+strconv.FormatInt(staticMin, 10), "static-max="+strconv.FormatInt(staticMax, 10),
		"dynamic-min="+strconv.FormatInt(dynamicMin, 10), "dynamic-max="+strconv.FormatInt(dynamicMax, 10))
	if err != nil {
		r := m.failure("vm-memory-limits-set", resolved, err)
		if strings.Contains(strings.ToLower(err.Error()), "dmc disabled") {
			r.Message = "Dynamic memory control (DMC) is disabled on this host, so static_max, dynamic_min and dynamic_max must be equal (static_min may be lower), for example pass memory_bytes to set all four to one value. (" + strings.TrimSpace(err.Error()) + ")"
		}
		return r, nil
	}
	return &ActionResult{Operation: "vm-memory-limits-set", Target: resolved, Success: true,
		Message: "Memory limits set.",
		Data:    map[string]int64{"static_min": staticMin, "static_max": staticMax, "dynamic_min": dynamicMin, "dynamic_max": dynamicMax}}, nil
}

// VMConsoleList lists a VM's consoles.
func (m *Module) VMConsoleList(ctx context.Context, uuid, name string) ([]ConsoleInfo, error) {
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, "console-list", "vm-uuid="+resolved, "params=uuid,protocol,location")
	if err != nil {
		return nil, err
	}
	blocks := parseXEBlocks(out)
	cons := make([]ConsoleInfo, 0, len(blocks))
	for _, b := range blocks {
		cons = append(cons, ConsoleInfo{UUID: b["uuid"], Protocol: b["protocol"], Location: b["location"]})
	}
	return cons, nil
}

// VMCreate creates a VM. With template (a uuid or name-label) it installs a
// new VM from that template (xe vm-install, copying the template's disks, to
// sr_uuid if given — can take long, so run it async). Without template it
// creates an empty VM record (xe vm-create) to be configured with
// vm.param_set and given disks. The new VM's uuid is in Data.uuid.
func (m *Module) VMCreate(ctx context.Context, newName, description, template, srUUID string) (*ActionResult, error) {
	if strings.TrimSpace(newName) == "" {
		return nil, fmt.Errorf("new_name is required")
	}
	var args []string
	op := "vm-create"
	if template != "" {
		op = "vm-install"
		tmpl := template
		if !uuidRe.MatchString(template) {
			out, err := m.run(ctx, "template-list", "name-label="+template, "params=uuid", "--minimal")
			if err != nil {
				return nil, fmt.Errorf("resolving template %q: %w", template, err)
			}
			var perr error
			if tmpl, perr = pickUUID(out, fmt.Sprintf("template name-label %q", template)); perr != nil {
				return nil, perr
			}
		}
		args = []string{"vm-install", "template=" + tmpl, "new-name-label=" + newName}
		if description != "" {
			args = append(args, "new-name-description="+description)
		}
		if srUUID != "" {
			args = append(args, "sr-uuid="+srUUID)
		}
	} else {
		args = []string{"vm-create", "name-label=" + newName}
		if description != "" {
			args = append(args, "name-description="+description)
		}
	}
	out, err := m.runTimeout(ctx, timeoutLong, args...)
	if err != nil {
		return m.failure(op, newName, err), nil
	}
	id := strings.TrimSpace(out)
	return &ActionResult{Operation: op, Target: newName, Success: true, Message: "VM created.", Output: id,
		Data: map[string]string{"uuid": id}}, nil
}

// VMClone clones a VM (fast copy-on-write on SRs that support it, a full copy
// otherwise — run it async for large VMs). The clone's uuid is in Data.uuid.
func (m *Module) VMClone(ctx context.Context, uuid, name, newName, description string) (*ActionResult, error) {
	if strings.TrimSpace(newName) == "" {
		return nil, fmt.Errorf("new_name is required")
	}
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	args := []string{"vm-clone", "uuid=" + resolved, "new-name-label=" + newName}
	if description != "" {
		args = append(args, "new-name-description="+description)
	}
	out, err := m.runTimeout(ctx, timeoutLong, args...)
	if err != nil {
		r := m.failure("vm-clone", resolved, err)
		var xe *XEError
		if asXE(err, &xe) {
			vmName, state := m.vmNameAndState(ctx, resolved)
			if vmName == "" {
				vmName = resolved
			}
			r.Message = friendlyVMPowerError("vm-clone", vmName, state, false, xe)
		}
		return r, nil
	}
	id := strings.TrimSpace(out)
	return &ActionResult{Operation: "vm-clone", Target: resolved, Success: true, Message: "VM cloned.", Output: id,
		Data: map[string]string{"uuid": id}}, nil
}

// VMDestroy deletes a halted VM's record. Its disks (VDIs) are left on the SR
// (xe vm-destroy semantics) and must be removed separately. Because this is
// irreversible, confirm must be true and expectedName must equal the VM's
// current name-label (guards against a wrong uuid); control domains are refused.
func (m *Module) VMDestroy(ctx context.Context, uuid, name string, confirm bool, expectedName string) (*ActionResult, error) {
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	if !confirm {
		return nil, fmt.Errorf("destroying VM %s is irreversible: pass confirm=true", resolved)
	}
	if expectedName == "" {
		return nil, fmt.Errorf("destroying VM %s needs expected_name: the VM's exact current name-label", resolved)
	}
	out, lerr := m.run(ctx, "vm-list", "uuid="+resolved, "params=name-label,power-state,is-control-domain")
	if lerr != nil {
		return nil, fmt.Errorf("reading VM %s: %w", resolved, lerr)
	}
	b := parseXEBlocks(out)
	if len(b) == 0 {
		return nil, fmt.Errorf("VM %s not found", resolved)
	}
	if b[0]["is-control-domain"] == "true" {
		return nil, fmt.Errorf("refusing to destroy control domain %s", resolved)
	}
	if b[0]["name-label"] != expectedName {
		return nil, fmt.Errorf("refusing to destroy VM %s: its name-label is %q, not expected_name %q",
			resolved, b[0]["name-label"], expectedName)
	}
	if _, err := m.run(ctx, "vm-destroy", "uuid="+resolved); err != nil {
		r := m.failure("vm-destroy", resolved, err)
		var xe *XEError
		if asXE(err, &xe) {
			r.Message = friendlyVMPowerError("vm-destroy", b[0]["name-label"], b[0]["power-state"], false, xe)
		}
		return r, nil
	}
	return &ActionResult{Operation: "vm-destroy", Target: resolved, Success: true,
		Message: fmt.Sprintf("VM %q destroyed; its disks were left on the storage repository.", b[0]["name-label"])}, nil
}

// VMPause pauses a running VM (frozen in memory, unlike suspend which writes to disk).
func (m *Module) VMPause(ctx context.Context, uuid, name string) (*ActionResult, error) {
	return m.vmAction(ctx, "vm-pause", uuid, name, false)
}

// VMUnpause resumes a paused VM.
func (m *Module) VMUnpause(ctx context.Context, uuid, name string) (*ActionResult, error) {
	return m.vmAction(ctx, "vm-unpause", uuid, name, false)
}

// SRCreateIfMissing is SRCreate that does nothing when an SR with the same
// name-label already exists: it then succeeds, with that SR's uuid in Data.uuid
// and Data.existing=true. Intended for idempotent provisioning (for example
// mounting an ISO repository on every compute member).
func (m *Module) SRCreateIfMissing(ctx context.Context, name, srType string, shared bool, contentType, hostUUID string, deviceConfig map[string]string) (*ActionResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name (name_label) is required")
	}
	existing, err := m.run(ctx, "sr-list", "name-label="+name, "--minimal")
	if err != nil {
		return nil, fmt.Errorf("checking for an existing SR %q: %w", name, err)
	}
	if ids := splitMinimalList(existing); len(ids) > 0 {
		return &ActionResult{Operation: "sr-create", Target: name, Success: true,
			Message: fmt.Sprintf("SR %q already exists; nothing created.", name), Output: ids[0],
			Data: map[string]any{"uuid": ids[0], "existing": true}}, nil
	}
	r, err := m.SRCreate(ctx, name, srType, shared, contentType, hostUUID, deviceConfig)
	if r != nil && r.Success {
		r.Data = map[string]any{"uuid": r.Output, "existing": false}
	}
	return r, err
}
