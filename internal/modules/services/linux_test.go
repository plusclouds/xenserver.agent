//go:build linux

package services

import (
	"errors"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
)

func TestIsUnknownMethod(t *testing.T) {
	if !isUnknownMethod(errors.New("Unknown method 'ListUnitsByNames' or interface 'org.freedesktop.systemd1.Manager'.")) {
		t.Error("the old-systemd error must be recognised")
	}
	if isUnknownMethod(errors.New("Access denied")) || isUnknownMethod(nil) {
		t.Error("other errors must not trigger the fallback")
	}
}

func TestPickUnits(t *testing.T) {
	all := []dbus.UnitStatus{{Name: "atd.service", ActiveState: "active"}, {Name: "crond.service"}}
	if got := pickUnits(all, "atd.service"); len(got) != 1 || got[0].ActiveState != "active" {
		t.Errorf("got %v", got)
	}
	if got := pickUnits(all, "nope.service"); len(got) != 0 {
		t.Errorf("unknown unit must give no match: %v", got)
	}
}

func TestUnitFileStates(t *testing.T) {
	files := []dbus.UnitFile{
		{Path: "/usr/lib/systemd/system/crond.service", Type: "enabled"},
		{Path: "/usr/lib/systemd/system/atd.service", Type: "disabled"},
		{Path: "/etc/systemd/system/x.service", Type: "enabled-runtime"},
		{Path: "/usr/lib/systemd/system/dbus.service", Type: "static"},
	}
	states := unitFileStateMap(files)
	for name, want := range map[string]struct {
		enabled bool
		state   string
	}{"crond.service": {true, "enabled"}, "atd.service": {false, "disabled"}, "x.service": {true, "enabled-runtime"}, "dbus.service": {false, "static"}} {
		info := ServiceInfo{Name: name}
		applyUnitFileState(&info, states)
		if info.Enabled != want.enabled || info.UnitFileState != want.state {
			t.Errorf("%s: enabled=%v state=%q, want %v %q", name, info.Enabled, info.UnitFileState, want.enabled, want.state)
		}
	}
	unknown := ServiceInfo{Name: "ghost.service"}
	applyUnitFileState(&unknown, states)
	if unknown.Enabled || unknown.UnitFileState != "" {
		t.Errorf("a unit without a unit file must stay unknown: %+v", unknown)
	}
	applyUnitFileState(&unknown, nil) // must not panic
}
