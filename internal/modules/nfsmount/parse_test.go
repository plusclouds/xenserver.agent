package nfsmount

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestParseProcMounts(t *testing.T) {
	data := `sysfs /sys sysfs rw,nosuid,nodev,noexec 0 0
10.0.0.5:/export/vol1 /mnt/import nfs4 rw,relatime,vers=4.2 0 0
tmpfs /run tmpfs rw,nosuid 0 0
192.168.1.10:/data /mnt/data nfs rw,relatime,vers=3 0 0
`
	got := parseProcMounts(data)
	want := []MountInfo{
		{Server: "10.0.0.5", ExportPath: "/export/vol1", MountPoint: "/mnt/import", FSType: "nfs4"},
		{Server: "192.168.1.10", ExportPath: "/data", MountPoint: "/mnt/data", FSType: "nfs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseProcMounts() = %+v, want %+v", got, want)
	}
}

func TestParseProcMounts_NoNFSEntries(t *testing.T) {
	data := "sysfs /sys sysfs rw 0 0\ntmpfs /run tmpfs rw 0 0\n"
	if got := parseProcMounts(data); got != nil {
		t.Errorf("parseProcMounts() = %+v, want nil", got)
	}
}

func TestParseProcMounts_EmptyInput(t *testing.T) {
	if got := parseProcMounts(""); got != nil {
		t.Errorf("parseProcMounts(\"\") = %+v, want nil", got)
	}
}

func TestListFilesSortFilterAndGuard(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("big.XVA", 300)
	write("small.xva", 10)
	write("mid.xva", 100)
	write("notes.txt", 1)
	if err := os.Mkdir(filepath.Join(dir, "subdir.xva"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := New(nil, "", "", zap.NewNop())

	// Unmounted path must be refused (it is a temp dir, not an NFS mount).
	if _, err := m.ListFiles(context.Background(), dir, "", "", false, 0); err == nil {
		t.Fatal("expected error for non-NFS mount point")
	}

	// Exercise the listing logic directly by faking the mount lookup.
	files, err := listFilesIn(dir, ".xva", SortSize, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range files {
		got = append(got, f.Name)
	}
	want := []string{"small.xva", "mid.xva", "big.XVA"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("size asc: got %v want %v", got, want)
	}
	files, _ = listFilesIn(dir, ".xva", SortSize, true, 1)
	if len(files) != 1 || files[0].Name != "big.XVA" {
		t.Fatalf("size desc limit 1: got %v", files)
	}
}
