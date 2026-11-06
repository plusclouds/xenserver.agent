package nfsmount

import "testing"

func TestValidateMountArgs(t *testing.T) {
	good := [][4]string{
		{"10.1.32.180", "/storage", "/mnt/x", ""},
		{"nas.example.com", "/a/b", "/mnt/y", "vers=3,soft,timeo=100"},
		{"fe80::1", "/e", "/mnt/z", "ro"},
	}
	for _, g := range good {
		if err := validateMountArgs(g[0], g[1], g[2], g[3]); err != nil {
			t.Errorf("%v rejected: %v", g, err)
		}
	}
	bad := [][4]string{
		{"-evil", "/s", "/mnt/x", ""},
		{"host", "relative", "/mnt/x", ""},
		{"host", "/s", "-l", ""},
		{"host", "/s", "relative/dir", ""},
		{"host", "/s", "/mnt/x", "-o"},
		{"host", "/s", "/mnt/x", "ro;rm"},
		{"ho st", "/s", "/mnt/x", ""},
	}
	for _, b := range bad {
		if err := validateMountArgs(b[0], b[1], b[2], b[3]); err == nil {
			t.Errorf("%v should be rejected", b)
		}
	}
	if validateMountPoint("-f") == nil {
		t.Error("flag-like mount point accepted")
	}
}
