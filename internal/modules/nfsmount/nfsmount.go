// Package nfsmount provides generic NFS mount/unmount management, used to
// stage VM export files for internal/modules/xenserver's VMImport/VMExport.
// Not XenServer-specific.
package nfsmount

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/executor"
)

// MountInfo describes one currently-mounted NFS filesystem.
type MountInfo struct {
	Server     string `json:"server"`
	ExportPath string `json:"export_path"`
	MountPoint string `json:"mount_point"`
	FSType     string `json:"fs_type"`
}

// ActionResult is the outcome of a state-changing nfsmount.* operation.
type ActionResult struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Success   bool   `json:"success"`
	Message   string `json:"message"`
}

// Module manages NFS mounts via the mount/umount binaries. Mount points are
// not confined to a configured base directory — the platform may mount
// wherever it asks. This is a deliberate choice, not an oversight.
type Module struct {
	exec         *executor.Executor
	mountBinary  string
	umountBinary string
	logger       *zap.Logger
}

// New creates an nfsmount Module.
func New(exec *executor.Executor, mountBinary, umountBinary string, logger *zap.Logger) *Module {
	return &Module{exec: exec, mountBinary: mountBinary, umountBinary: umountBinary, logger: logger}
}

// Mount mounts server:exportPath at mountPoint, creating mountPoint if it
// doesn't already exist. options, if non-empty, is passed as "-o <options>".
func (m *Module) Mount(ctx context.Context, server, exportPath, mountPoint, options string) (*ActionResult, error) {
	if server == "" || exportPath == "" || mountPoint == "" {
		return nil, fmt.Errorf("server, export_path, and mount_point are required")
	}

	if err := validateMountArgs(server, exportPath, mountPoint, options); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return &ActionResult{Operation: "mount", Target: mountPoint, Success: false,
			Message: fmt.Sprintf("creating mount point: %s", err)}, nil
	}

	source := server + ":" + exportPath
	args := []string{"-t", "nfs"}
	if options != "" {
		args = append(args, "-o", options)
	}
	args = append(args, "--", source, mountPoint)

	_, stderr, err := m.exec.Execute(ctx, m.mountBinary, args...)
	if err != nil {
		return &ActionResult{Operation: "mount", Target: mountPoint, Success: false,
			Message: firstNonEmpty(strings.TrimSpace(stderr), err.Error())}, nil
	}
	return &ActionResult{Operation: "mount", Target: mountPoint, Success: true,
		Message: fmt.Sprintf("mounted %s at %s", source, mountPoint)}, nil
}

// Unmount unmounts mountPoint.
func (m *Module) Unmount(ctx context.Context, mountPoint string) (*ActionResult, error) {
	if mountPoint == "" {
		return nil, fmt.Errorf("mount_point is required")
	}
	if err := validateMountPoint(mountPoint); err != nil {
		return nil, err
	}
	_, stderr, err := m.exec.Execute(ctx, m.umountBinary, "--", mountPoint)
	if err != nil {
		return &ActionResult{Operation: "unmount", Target: mountPoint, Success: false,
			Message: firstNonEmpty(strings.TrimSpace(stderr), err.Error())}, nil
	}
	return &ActionResult{Operation: "unmount", Target: mountPoint, Success: true,
		Message: fmt.Sprintf("unmounted %s", mountPoint)}, nil
}

// List returns every currently-mounted NFS filesystem, parsed from
// /proc/mounts.
func (m *Module) List(_ context.Context) ([]MountInfo, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, fmt.Errorf("reading /proc/mounts: %w", err)
	}
	return parseProcMounts(string(data)), nil
}

// FileInfo describes one regular file found directly under a mount point.
type FileInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Modified  string `json:"modified"`
}

// Sort keys accepted by ListFiles.
const (
	SortName  = "name"
	SortSize  = "size"
	SortMTime = "mtime"
)

// ListFiles lists the regular files directly under mountPoint (no recursion),
// so the platform can browse an image repository without a generic exec.
// mountPoint must be a currently-mounted NFS mount point — this is not a
// general directory lister. suffix, if non-empty, keeps only names ending in
// it (case-insensitive). sortBy is name (default), size or mtime; descending
// reverses it. limit <= 0 means no limit.
func (m *Module) ListFiles(ctx context.Context, mountPoint, suffix, sortBy string, descending bool, limit int) ([]FileInfo, error) {
	if mountPoint == "" {
		return nil, fmt.Errorf("mount_point is required")
	}
	mountPoint = filepath.Clean(mountPoint)
	mounts, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	mounted := false
	for _, mi := range mounts {
		if mi.MountPoint == mountPoint {
			mounted = true
			break
		}
	}
	if !mounted {
		return nil, fmt.Errorf("%s is not a mounted NFS mount point (mount it with nfsmount.mount first)", mountPoint)
	}
	return listFilesIn(mountPoint, suffix, sortBy, descending, limit)
}

// listFilesIn does the actual directory listing, filtering and sorting.
func listFilesIn(mountPoint, suffix, sortBy string, descending bool, limit int) ([]FileInfo, error) {
	switch sortBy {
	case "":
		sortBy = SortName
	case SortName, SortSize, SortMTime:
	default:
		return nil, fmt.Errorf("sort_by must be one of: name, size, mtime")
	}

	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", mountPoint, err)
	}
	suffix = strings.ToLower(suffix)
	files := make([]FileInfo, 0, len(entries))
	var mtimes = map[string]time.Time{}
	for _, e := range entries {
		if suffix != "" && !strings.HasSuffix(strings.ToLower(e.Name()), suffix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		mtimes[e.Name()] = info.ModTime()
		files = append(files, FileInfo{
			Name:      e.Name(),
			Path:      filepath.Join(mountPoint, e.Name()),
			SizeBytes: info.Size(),
			Modified:  info.ModTime().UTC().Format(time.RFC3339),
		})
	}

	cmp := func(a, b FileInfo) int {
		var c int
		switch sortBy {
		case SortSize:
			switch {
			case a.SizeBytes < b.SizeBytes:
				c = -1
			case a.SizeBytes > b.SizeBytes:
				c = 1
			}
		case SortMTime:
			switch {
			case mtimes[a.Name].Before(mtimes[b.Name]):
				c = -1
			case mtimes[a.Name].After(mtimes[b.Name]):
				c = 1
			}
		}
		if c == 0 {
			c = strings.Compare(a.Name, b.Name) // stable tie-break
		}
		if descending {
			c = -c
		}
		return c
	}
	sort.SliceStable(files, func(i, j int) bool { return cmp(files[i], files[j]) < 0 })
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var (
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]*[A-Za-z0-9])?$`)
	optionsRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_=.,:/-]*$`)
)

func validateMountPoint(mountPoint string) error {
	if !filepath.IsAbs(mountPoint) {
		return fmt.Errorf("mount_point %q must be an absolute path", mountPoint)
	}
	if strings.ContainsAny(mountPoint, "\x00\n") {
		return fmt.Errorf("mount_point contains invalid characters")
	}
	return nil
}

// validateMountArgs rejects values that mount(8) could read as options or that
// are not a plausible NFS server, export path or mount option list.
func validateMountArgs(server, exportPath, mountPoint, options string) error {
	if !hostRe.MatchString(server) {
		return fmt.Errorf("server %q must be a hostname or IP address", server)
	}
	if !strings.HasPrefix(exportPath, "/") || strings.ContainsAny(exportPath, "\x00\n") {
		return fmt.Errorf("export_path %q must be an absolute path", exportPath)
	}
	if err := validateMountPoint(mountPoint); err != nil {
		return err
	}
	if options != "" && !optionsRe.MatchString(options) {
		return fmt.Errorf("mount options %q contain invalid characters (allowed: letters, digits, and _ = . , : / -)", options)
	}
	return nil
}
