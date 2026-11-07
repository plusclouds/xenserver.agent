package xenserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Limits for generated config-drive ISOs.
const (
	isoMaxFiles     = 32
	isoMaxFileBytes = 1 << 20 // 1 MiB each
	isoMaxTotal     = 4 << 20 // 4 MiB overall
)

var (
	isoNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)
	isoLabelRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	isoMemberRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// isoDir returns the directory where an ISO storage repository's files live
// on this host, after confirming the SR exists and is an ISO SR.
func (m *Module) isoDir(ctx context.Context, srUUID string) (string, error) {
	if srUUID == "" {
		return "", fmt.Errorf("sr_uuid (an ISO storage repository) is required")
	}
	typ, err := m.run(ctx, "sr-param-get", "uuid="+srUUID, "param-name=type")
	if err != nil {
		return "", fmt.Errorf("storage repository %s does not exist on this host", srUUID)
	}
	if strings.TrimSpace(typ) != "iso" {
		return "", fmt.Errorf("storage repository %s is of type %q, not an ISO library (type iso)", srUUID, strings.TrimSpace(typ))
	}
	base := m.srMountDir
	if base == "" {
		base = "/run/sr-mount"
	}
	dir := filepath.Join(base, srUUID)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("ISO library %s is not attached on this host (%s is missing); plug its PBD first", srUUID, dir)
	}
	return dir, nil
}

func (m *Module) isoTool() (string, error) {
	if m.isoBinary != "" {
		if _, err := os.Stat(m.isoBinary); err == nil {
			return m.isoBinary, nil
		}
	}
	return "", fmt.Errorf("ISO builder %q not found on this host; install genisoimage (or mkisofs) or set xenserver.iso_binary", m.isoBinary)
}

// ISOCreate builds a small ISO image (for example a cloud-init "cidata" config
// drive) from the given files and writes it into an ISO storage repository,
// then rescans the SR. The new disk image uuid is returned in Data.vdi_uuid.
func (m *Module) ISOCreate(ctx context.Context, srUUID, name string, files map[string]string, label string, overwrite bool) (*ActionResult, error) {
	if !strings.HasSuffix(strings.ToLower(name), ".iso") {
		name += ".iso"
	}
	if !isoNameRe.MatchString(name) {
		return nil, fmt.Errorf("name %q is invalid: use letters, digits, '.', '_' and '-' (max 100 characters)", name)
	}
	if label == "" {
		label = "cidata"
	}
	if !isoLabelRe.MatchString(label) {
		return nil, fmt.Errorf("volume_label %q is invalid: use 1-32 letters, digits, '_' or '-'", label)
	}
	if len(files) == 0 || len(files) > isoMaxFiles {
		return nil, fmt.Errorf("files must contain between 1 and %d entries", isoMaxFiles)
	}
	total := 0
	for fn, content := range files {
		if !isoMemberRe.MatchString(fn) {
			return nil, fmt.Errorf("file name %q is invalid: use letters, digits, '.', '_' and '-' (no directories)", fn)
		}
		if len(content) > isoMaxFileBytes {
			return nil, fmt.Errorf("file %q is larger than %d bytes", fn, isoMaxFileBytes)
		}
		total += len(content)
	}
	if total > isoMaxTotal {
		return nil, fmt.Errorf("files total %d bytes; limit is %d", total, isoMaxTotal)
	}
	dir, err := m.isoDir(ctx, srUUID)
	if err != nil {
		return nil, err
	}
	tool, err := m.isoTool()
	if err != nil {
		return nil, err
	}
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err == nil && !overwrite {
		return nil, fmt.Errorf("%s already exists in the ISO library; pass overwrite=true to replace it", name)
	}

	// Stage the files in a private temp dir, build next to the target under a
	// dotted temporary name (ignored by the SR scan), then rename into place.
	stage, err := os.MkdirTemp("", "plusclouds-iso-")
	if err != nil {
		return nil, fmt.Errorf("creating staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	names := make([]string, 0, len(files))
	for fn, content := range files {
		if err := os.WriteFile(filepath.Join(stage, fn), []byte(content), 0o600); err != nil {
			return nil, fmt.Errorf("staging %s: %w", fn, err)
		}
		names = append(names, fn)
	}
	sort.Strings(names)
	tmp := filepath.Join(dir, "."+name+".tmp")
	defer os.Remove(tmp)
	if _, stderr, err := m.exec.Execute(ctx, tool, "-quiet", "-input-charset", "utf-8", "-o", tmp, "-V", label, "-J", "-R", stage); err != nil {
		return &ActionResult{Operation: "iso-create", Target: name, Success: false,
			Message: fmt.Sprintf("building the ISO failed: %s", firstNonEmptyStr(strings.TrimSpace(stderr), err.Error()))}, nil
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return nil, fmt.Errorf("setting permissions on the ISO: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return nil, fmt.Errorf("placing the ISO in the library: %w", err)
	}
	if _, err := m.runTimeout(ctx, timeoutMedium, "sr-scan", "uuid="+srUUID); err != nil {
		r := m.failure("iso-create", name, err)
		r.Message = "ISO written to the library but rescanning the SR failed: " + r.Message
		return r, nil
	}
	vdi := ""
	if out, err := m.run(ctx, "vdi-list", "sr-uuid="+srUUID, "name-label="+name, "params=uuid", "--minimal"); err == nil {
		if ids := splitMinimalList(out); len(ids) == 1 {
			vdi = ids[0]
		}
	}
	return &ActionResult{Operation: "iso-create", Target: name, Success: true,
		Message: fmt.Sprintf("ISO %s (label %q, files %s) created in library %s.", name, label, strings.Join(names, ", "), srUUID),
		Output:  vdi, Data: map[string]any{"vdi_uuid": vdi, "name": name, "sr_uuid": srUUID, "volume_label": label, "files": names}}, nil
}

// ISORemove deletes an ISO from an ISO storage repository and rescans it. An
// ISO that is inserted in a VM's drive is refused.
func (m *Module) ISORemove(ctx context.Context, srUUID, name string, confirm bool) (*ActionResult, error) {
	if !confirm {
		return nil, fmt.Errorf("removing %q deletes the file and is irreversible: pass confirm=true", name)
	}
	if !strings.HasSuffix(strings.ToLower(name), ".iso") {
		name += ".iso"
	}
	if !isoNameRe.MatchString(name) {
		return nil, fmt.Errorf("name %q is invalid", name)
	}
	dir, err := m.isoDir(ctx, srUUID)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err != nil {
		return nil, fmt.Errorf("%s does not exist in the ISO library", name)
	}
	if out, err := m.run(ctx, "vdi-list", "sr-uuid="+srUUID, "name-label="+name, "params=uuid", "--minimal"); err == nil {
		for _, vdi := range splitMinimalList(out) {
			if used, uerr := m.run(ctx, "vbd-list", "vdi-uuid="+vdi, "currently-attached=true", "--minimal"); uerr == nil && strings.TrimSpace(used) != "" {
				return nil, fmt.Errorf("%s is inserted in a running VM's CD drive; eject it first (xenserver.vm.cd_eject)", name)
			}
		}
	}
	if err := os.Remove(target); err != nil {
		return nil, fmt.Errorf("deleting %s: %w", name, err)
	}
	if _, err := m.runTimeout(ctx, timeoutMedium, "sr-scan", "uuid="+srUUID); err != nil {
		r := m.failure("iso-remove", name, err)
		r.Message = "ISO deleted but rescanning the SR failed: " + r.Message
		return r, nil
	}
	return &ActionResult{Operation: "iso-remove", Target: name, Success: true,
		Message: fmt.Sprintf("ISO %s removed from library %s.", name, srUUID)}, nil
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
