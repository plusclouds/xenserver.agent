//go:build linux

package services

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/coreos/go-systemd/v22/dbus"
	"go.uber.org/zap"
)

// linuxManager implements Manager via systemd D-Bus.
type linuxManager struct {
	bus    *dbus.Conn
	logger *zap.Logger
}

// New returns a Manager backed by systemd D-Bus.
// bus may be nil — in that case every operation returns an error explaining
// that D-Bus is unavailable (graceful degradation).
func New(bus *dbus.Conn, logger *zap.Logger) Manager {
	return &linuxManager{bus: bus, logger: logger}
}

func ensureServiceSuffix(name string) string {
	if !strings.Contains(name, ".") {
		return name + ".service"
	}
	return name
}

func (m *linuxManager) List(ctx context.Context) ([]ServiceInfo, error) {
	if m.bus == nil {
		return nil, fmt.Errorf("systemd D-Bus is not available")
	}
	units, err := m.bus.ListUnitsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing systemd units: %w", err)
	}
	fileStates := m.unitFileStates(ctx)
	services := make([]ServiceInfo, 0, len(units))
	for _, u := range units {
		if !strings.HasSuffix(u.Name, ".service") {
			continue
		}
		info := unitToServiceInfo(u)
		applyUnitFileState(&info, fileStates)
		services = append(services, info)
	}
	return services, nil
}

func (m *linuxManager) Get(ctx context.Context, name string) (*ServiceInfo, error) {
	if m.bus == nil {
		return nil, fmt.Errorf("systemd D-Bus is not available")
	}
	name = ensureServiceSuffix(name)
	units, err := m.bus.ListUnitsByNamesContext(ctx, []string{name})
	if err != nil {
		if !isUnknownMethod(err) {
			return nil, fmt.Errorf("querying unit %s: %w", name, err)
		}
		// Older systemd (e.g. v219 on XenServer/XCP-ng 8.x) has no
		// ListUnitsByNames; fall back to the plain list, which every version has.
		all, lerr := m.bus.ListUnitsContext(ctx)
		if lerr != nil {
			return nil, fmt.Errorf("listing systemd units: %w", lerr)
		}
		units = pickUnits(all, name)
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("unit not found (or not loaded by systemd): %s", name)
	}
	info := unitToServiceInfo(units[0])
	applyUnitFileState(&info, m.unitFileStates(ctx))
	return &info, nil
}

// unitFileStates maps unit name to systemd's unit-file state ("enabled",
// "disabled", "static", ...). Best effort: nil if it cannot be read.
func (m *linuxManager) unitFileStates(ctx context.Context) map[string]string {
	files, err := m.bus.ListUnitFilesContext(ctx)
	if err != nil {
		m.logger.Debug("could not read unit file states", zap.Error(err))
		return nil
	}
	return unitFileStateMap(files)
}

// unitFileStateMap indexes unit files by unit name.
func unitFileStateMap(files []dbus.UnitFile) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[filepath.Base(f.Path)] = f.Type
	}
	return out
}

// applyUnitFileState fills Enabled and UnitFileState from the state map.
func applyUnitFileState(info *ServiceInfo, states map[string]string) {
	st, ok := states[info.Name]
	if !ok {
		return
	}
	info.UnitFileState = st
	info.Enabled = st == "enabled" || st == "enabled-runtime"
}

// isUnknownMethod reports whether err is systemd saying the D-Bus method does
// not exist on this version.
func isUnknownMethod(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Unknown method")
}

// pickUnits returns the entries of all whose name equals name.
func pickUnits(all []dbus.UnitStatus, name string) []dbus.UnitStatus {
	var out []dbus.UnitStatus
	for _, u := range all {
		if u.Name == name {
			out = append(out, u)
		}
	}
	return out
}

func (m *linuxManager) Start(ctx context.Context, name string) (*ActionResult, error) {
	if m.bus == nil {
		return failResult(name, ActionStart, fmt.Errorf("systemd D-Bus is not available")), nil
	}
	name = ensureServiceSuffix(name)
	m.logger.Info("starting service", zap.String("unit", name))
	ch := make(chan string, 1)
	if _, err := m.bus.StartUnitContext(ctx, name, "replace", ch); err != nil {
		return failResult(name, ActionStart, fmt.Errorf("start failed: %w", err)), nil
	}
	if result := <-ch; result != "done" {
		return failResult(name, ActionStart, fmt.Errorf("job result: %s", result)), nil
	}
	return &ActionResult{Service: name, Action: ActionStart, Success: true,
		Message: fmt.Sprintf("Service %s started successfully.", name)}, nil
}

func (m *linuxManager) Stop(ctx context.Context, name string) (*ActionResult, error) {
	if m.bus == nil {
		return failResult(name, ActionStop, fmt.Errorf("systemd D-Bus is not available")), nil
	}
	name = ensureServiceSuffix(name)
	m.logger.Info("stopping service", zap.String("unit", name))
	ch := make(chan string, 1)
	if _, err := m.bus.StopUnitContext(ctx, name, "replace", ch); err != nil {
		return failResult(name, ActionStop, fmt.Errorf("stop failed: %w", err)), nil
	}
	if result := <-ch; result != "done" {
		return failResult(name, ActionStop, fmt.Errorf("job result: %s", result)), nil
	}
	return &ActionResult{Service: name, Action: ActionStop, Success: true,
		Message: fmt.Sprintf("Service %s stopped successfully.", name)}, nil
}

func (m *linuxManager) Restart(ctx context.Context, name string) (*ActionResult, error) {
	if m.bus == nil {
		return failResult(name, ActionRestart, fmt.Errorf("systemd D-Bus is not available")), nil
	}
	name = ensureServiceSuffix(name)
	m.logger.Info("restarting service", zap.String("unit", name))
	ch := make(chan string, 1)
	if _, err := m.bus.RestartUnitContext(ctx, name, "replace", ch); err != nil {
		return failResult(name, ActionRestart, fmt.Errorf("restart failed: %w", err)), nil
	}
	if result := <-ch; result != "done" {
		return failResult(name, ActionRestart, fmt.Errorf("job result: %s", result)), nil
	}
	return &ActionResult{Service: name, Action: ActionRestart, Success: true,
		Message: fmt.Sprintf("Service %s restarted successfully.", name)}, nil
}

func (m *linuxManager) Enable(ctx context.Context, name string) (*ActionResult, error) {
	if m.bus == nil {
		return failResult(name, ActionEnable, fmt.Errorf("systemd D-Bus is not available")), nil
	}
	name = ensureServiceSuffix(name)
	m.logger.Info("enabling service", zap.String("unit", name))
	if _, _, err := m.bus.EnableUnitFilesContext(ctx, []string{name}, false, true); err != nil {
		return failResult(name, ActionEnable, fmt.Errorf("enable failed: %w", err)), nil
	}
	if err := m.bus.ReloadContext(ctx); err != nil {
		m.logger.Warn("daemon-reload after enable failed", zap.Error(err))
	}
	return &ActionResult{Service: name, Action: ActionEnable, Success: true,
		Message: fmt.Sprintf("Service %s enabled successfully.", name)}, nil
}

func (m *linuxManager) Disable(ctx context.Context, name string) (*ActionResult, error) {
	if m.bus == nil {
		return failResult(name, ActionDisable, fmt.Errorf("systemd D-Bus is not available")), nil
	}
	name = ensureServiceSuffix(name)
	m.logger.Info("disabling service", zap.String("unit", name))
	if _, err := m.bus.DisableUnitFilesContext(ctx, []string{name}, false); err != nil {
		return failResult(name, ActionDisable, fmt.Errorf("disable failed: %w", err)), nil
	}
	if err := m.bus.ReloadContext(ctx); err != nil {
		m.logger.Warn("daemon-reload after disable failed", zap.Error(err))
	}
	return &ActionResult{Service: name, Action: ActionDisable, Success: true,
		Message: fmt.Sprintf("Service %s disabled successfully.", name)}, nil
}

func (m *linuxManager) Reload(ctx context.Context, name string) (*ActionResult, error) {
	if m.bus == nil {
		return failResult(name, ActionReload, fmt.Errorf("systemd D-Bus is not available")), nil
	}
	name = ensureServiceSuffix(name)
	m.logger.Info("reloading service", zap.String("unit", name))
	ch := make(chan string, 1)
	if _, err := m.bus.ReloadUnitContext(ctx, name, "replace", ch); err != nil {
		return failResult(name, ActionReload, fmt.Errorf("reload failed: %w", err)), nil
	}
	if result := <-ch; result != "done" {
		return failResult(name, ActionReload, fmt.Errorf("job result: %s", result)), nil
	}
	return &ActionResult{Service: name, Action: ActionReload, Success: true,
		Message: fmt.Sprintf("Service %s reloaded successfully.", name)}, nil
}

func unitToServiceInfo(unit dbus.UnitStatus) ServiceInfo {
	state := StateUnknown
	switch unit.ActiveState {
	case "active":
		state = StateActive
	case "inactive":
		state = StateInactive
	case "failed":
		state = StateFailed
	}
	return ServiceInfo{
		Name:        unit.Name,
		Description: unit.Description,
		State:       state,
		SubState:    unit.SubState,
	}
}

func failResult(name string, action ServiceAction, err error) *ActionResult {
	return &ActionResult{Service: name, Action: action, Success: false, Message: err.Error()}
}
