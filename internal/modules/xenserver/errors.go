package xenserver

import (
	"context"
	"errors"
	"fmt"
	"github.com/plusclouds/ubuntu-agent/internal/executor"
	"regexp"
	"strconv"
	"strings"
)

// XEError is a failed xe invocation with everything needed to debug it:
// the command line, exit code, raw stderr/stdout and, when xapi supplied
// them, the structured error code and parameters.
type XEError struct {
	Command   string
	ExitCode  int
	Stderr    string
	Stdout    string
	ErrorCode string   // e.g. SR_BACKEND_FAILURE_88, from an "Error code:" line
	ErrorArgs []string // from an "Error parameters:" line
	cause     error
}

func (e *XEError) Error() string {
	if s := strings.TrimSpace(e.Stderr); s != "" {
		return fmt.Sprintf("%v: %s", e.cause, s)
	}
	return e.cause.Error()
}

func (e *XEError) Unwrap() error { return e.cause }

// ErrorDetail is the machine-readable error context attached to a failed
// ActionResult, alongside the human Message.
type ErrorDetail struct {
	Command   string   `json:"command"`
	ExitCode  int      `json:"exit_code"`
	ErrorCode string   `json:"error_code,omitempty"`
	ErrorArgs []string `json:"error_args,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	Stdout    string   `json:"stdout,omitempty"`
}

var (
	exitCodeRe  = regexp.MustCompile(`exit (-?\d+)`)
	errorCodeRe = regexp.MustCompile(`(?m)^Error code:\s*(\S+)`)
	errorArgsRe = regexp.MustCompile(`(?m)^Error parameters:\s*(.*)$`)
)

// newXEError builds an XEError from a failed Execute call.
func newXEError(binary string, args []string, stdout, stderr string, cause error) *XEError {
	e := &XEError{
		Command:  commandLine(binary, args),
		ExitCode: -1,
		Stderr:   strings.TrimSpace(stderr),
		Stdout:   strings.TrimSpace(stdout),
		cause:    cause,
	}
	if m := exitCodeRe.FindStringSubmatch(cause.Error()); m != nil {
		e.ExitCode, _ = strconv.Atoi(m[1])
	}
	if m := errorCodeRe.FindStringSubmatch(stderr); m != nil {
		e.ErrorCode = m[1]
	}
	if m := errorArgsRe.FindStringSubmatch(stderr); m != nil {
		for _, a := range strings.Split(m[1], ",") {
			if a = strings.TrimSpace(a); a != "" {
				e.ErrorArgs = append(e.ErrorArgs, a)
			}
		}
	}
	return e
}

// commandLine renders the command for display, redacting secret-looking values.
func commandLine(binary string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, binary)
	parts = append(parts, executor.RedactArgs(args)...)
	return strings.Join(parts, " ")
}

// failure builds a failed ActionResult. Message is unchanged (err.Error());
// Detail carries the structured context when err came from an xe call.
func (m *Module) failure(op, target string, err error) *ActionResult {
	r := &ActionResult{Operation: op, Target: target, Success: false, Message: err.Error()}
	var xe *XEError
	if errors.As(err, &xe) {
		r.Detail = &ErrorDetail{
			Command: xe.Command, ExitCode: xe.ExitCode, ErrorCode: xe.ErrorCode,
			ErrorArgs: xe.ErrorArgs, Stderr: xe.Stderr, Stdout: xe.Stdout,
		}
	}
	return r
}

// diagnoseVMStart gathers best-effort context explaining why a VM could not
// start: configured memory/vCPUs vs. host free memory, whether each VIF's
// network has an attached interface, and whether each disk's SR is plugged.
// Every probe is independent; failures are noted, never fatal.
func (m *Module) diagnoseVMStart(ctx context.Context, vm string) []string {
	var d []string
	note := func(f string, a ...any) { d = append(d, fmt.Sprintf(f, a...)) }

	if out, err := m.run(ctx, "vm-list", "uuid="+vm, "params=memory-static-max,memory-dynamic-max,VCPUs-max,HVM-boot-policy,power-state"); err == nil {
		if b := parseXEBlocks(out); len(b) > 0 {
			note("VM config: memory-static-max=%s memory-dynamic-max=%s VCPUs-max=%s HVM-boot-policy=%q power-state=%s",
				b[0]["memory-static-max"], b[0]["memory-dynamic-max"], b[0]["VCPUs-max"], b[0]["HVM-boot-policy"], b[0]["power-state"])
			if host, herr := m.localHostUUID(); herr == nil {
				if free, ferr := m.run(ctx, "host-compute-free-memory", "uuid="+host); ferr == nil {
					need, _ := strconv.ParseInt(b[0]["memory-static-max"], 10, 64)
					have, _ := strconv.ParseInt(strings.TrimSpace(free), 10, 64)
					verdict := "enough"
					if need > have {
						verdict = "NOT ENOUGH"
					}
					note("Host free memory: %d bytes, VM needs up to %d bytes: %s", have, need, verdict)
				}
			}
		}
	} else {
		note("could not read VM config: %v", err)
	}

	if out, err := m.run(ctx, "vif-list", "vm-uuid="+vm, "params=device,network-uuid"); err == nil {
		seen := map[string]bool{}
		for _, b := range parseXEBlocks(out) {
			n := b["network-uuid"]
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			name, bridge := n, ""
			if nout, nerr := m.run(ctx, "network-list", "uuid="+n, "params=name-label,bridge"); nerr == nil {
				if nb := parseXEBlocks(nout); len(nb) > 0 {
					name, bridge = nb[0]["name-label"], nb[0]["bridge"]
				}
			}
			pout, perr := m.run(ctx, "pif-list", "network-uuid="+n, "params=device,currently-attached,bond-slave-of")
			if perr != nil {
				note("network %q (%s): could not list interfaces: %v", name, bridge, perr)
				continue
			}
			pifs := parseXEBlocks(pout)
			if len(pifs) == 0 {
				note("network %q (bridge %s, VIF devices on it share this): no physical interface (internal network)", name, bridge)
				continue
			}
			attached := 0
			var desc []string
			for _, p := range pifs {
				if p["currently-attached"] == "true" {
					attached++
				}
				desc = append(desc, fmt.Sprintf("%s attached=%s bond-slave-of=%s", p["device"], p["currently-attached"], p["bond-slave-of"]))
			}
			flag := "ok"
			if attached == 0 {
				flag = "PROBLEM: no attached interface — VIFs on this network cannot start"
			}
			note("network %q (bridge %s): %s [%s]", name, bridge, flag, strings.Join(desc, "; "))
		}
	}

	if out, err := m.run(ctx, "vbd-list", "vm-uuid="+vm, "type=Disk", "params=vdi-uuid"); err == nil {
		seen := map[string]bool{}
		for _, b := range parseXEBlocks(out) {
			vdi := b["vdi-uuid"]
			if vdi == "" || vdi == "<not in database>" {
				continue
			}
			vout, verr := m.run(ctx, "vdi-list", "uuid="+vdi, "params=sr-uuid")
			if verr != nil {
				continue
			}
			vb := parseXEBlocks(vout)
			if len(vb) == 0 || seen[vb[0]["sr-uuid"]] {
				continue
			}
			sr := vb[0]["sr-uuid"]
			seen[sr] = true
			pout, perr := m.run(ctx, "pbd-list", "sr-uuid="+sr, "params=currently-attached")
			if perr != nil {
				continue
			}
			ok := false
			for _, p := range parseXEBlocks(pout) {
				if p["currently-attached"] == "true" {
					ok = true
				}
			}
			if ok {
				note("SR %s: attached", sr)
			} else {
				note("SR %s: PROBLEM — not attached on any host", sr)
			}
		}
	}
	return d
}

var powerStateRe = regexp.MustCompile(`(?m)expected:\s*([^\n]+)\n\s*actual:\s*(\S+)`)

// friendlyVMPowerError turns a failed xe vm-* power operation into a clear
// sentence that says what was attempted, the VM's actual state and what to do.
// state is the VM's power-state read after the failure ("" if unknown). The raw
// xe text stays available in the result's Detail.
func friendlyVMPowerError(op, vmName, state string, force bool, xe *XEError) string {
	verbs := map[string]string{"vm-start": "start", "vm-shutdown": "shut down", "vm-reboot": "reboot",
		"vm-suspend": "suspend", "vm-resume": "resume", "vm-export": "export",
		"vm-pause": "pause", "vm-unpause": "unpause", "vm-destroy": "destroy", "vm-clone": "clone"}
	verb := verbs[op]
	if verb == "" {
		verb = strings.TrimPrefix(op, "vm-")
	}
	raw := xe.Stderr + "\n" + xe.Stdout
	lower := strings.ToLower(raw)
	if xe.cause != nil && strings.Contains(xe.cause.Error(), "timed out") {
		return fmt.Sprintf("Failed to %s VM %q: the agent gave up waiting for xe (%v). The VM is %s; check its state before retrying.",
			verb, vmName, xe.cause, orUnknown(state))
	}

	switch {
	case strings.Contains(lower, "not in an appropriate power state"):
		expected, actual := "", state
		if m := powerStateRe.FindStringSubmatch(raw); m != nil {
			expected, actual = joinStates(m[1]), m[2]
		}
		msg := fmt.Sprintf("Cannot %s VM %q: it is %s", verb, vmName, actual)
		if expected != "" {
			msg += fmt.Sprintf(", but it must be %s", expected)
		}
		msg += "."
		switch {
		case op == "vm-start" && actual == "running":
			msg += " It is already running."
		case op == "vm-destroy" && actual != "halted":
			msg += " Shut it down first."
		case op == "vm-unpause":
			msg += " Only a paused VM can be unpaused."
		case op == "vm-clone":
			msg += " Shut it down or snapshot it first."
		case op == "vm-export" && actual == "running":
			msg += " Shut it down first, or export a snapshot with xenserver.vm.snapshot_export."
		case op == "vm-resume":
			msg += " Only a suspended VM can be resumed; use start for a halted VM."
		case actual == "halted":
			msg += " Start it first."
		case actual == "suspended":
			msg += " Resume it first."
		}
		return msg

	case strings.Contains(lower, "lacks the feature"):
		msg := fmt.Sprintf("Cannot %s VM %q cleanly: it is %s but its Xen guest tools (management agent) are not installed or have not started yet (a freshly booted VM needs a minute or two), which this operation needs.",
			verb, vmName, orUnknown(state))
		if (op == "vm-shutdown" || op == "vm-reboot") && !force {
			msg += fmt.Sprintf(" Use xenserver.vm.force_%s for a hard %s, or install the guest tools in the VM.", strings.ReplaceAll(strings.TrimPrefix(op, "vm-"), " ", "_"), verb)
		} else {
			msg += " Install the guest tools in the VM."
		}
		return msg

	case strings.Contains(lower, "no matching vms found"):
		return fmt.Sprintf("Cannot %s VM %q: no such VM. If it is a template (for example a restored snapshot export), turn it into a VM first with xenserver.vm.promote_template.", verb, vmName)

	case op == "vm-start" && (strings.Contains(lower, "no servers available") || strings.Contains(lower, "no suitable hosts")):
		msg := fmt.Sprintf("Cannot start VM %q: no host can run it.", vmName)
		if reason := hostReasons(xe.Stdout); reason != "" {
			msg += " " + reason
		}
		return msg
	}
	if strings.TrimSpace(xe.Stderr) == "" && strings.TrimSpace(xe.Stdout) == "" {
		hint := ""
		if op == "vm-shutdown" || op == "vm-reboot" {
			hint = fmt.Sprintf(" This usually means the guest did not act on the clean %s in time (guest tools not ready); use xenserver.vm.force_%s for a hard one.",
				verb, strings.TrimPrefix(op, "vm-"))
		}
		return fmt.Sprintf("Failed to %s VM %q (power state: %s): xe exited with code %d and printed no error text.%s",
			verb, vmName, orUnknown(state), xe.ExitCode, hint)
	}
	return fmt.Sprintf("Failed to %s VM %q (power state: %s): %s", verb, vmName, orUnknown(state), strings.TrimSpace(xe.Stderr+" "+xe.Stdout))
}

// joinStates turns xapi's "paused, running" expected-state list into
// "paused or running".
func joinStates(list string) string {
	var parts []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " or ")
}

func orUnknown(s string) string {
	if s == "" {
		return "in an unknown state"
	}
	return s
}

// hostReasons extracts the per-host lines from xe's "no suitable hosts" table
// (e.g. "host1 : Cannot start here [Host cannot attach to network ...]").
func hostReasons(stdout string) string {
	var out []string
	for _, l := range strings.Split(stdout, "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, ":") && strings.Contains(l, "Cannot start here") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "; ")
}

// asXE reports whether err wraps an *XEError, storing it in target.
func asXE(err error, target **XEError) bool { return errors.As(err, target) }

// friendlyDeviceError explains a failed disk/CD/NIC operation: the VM's power
// state, a disk that is still in use, or a missing object. subject is the VM
// or disk name; state is the VM's power-state when known.
func friendlyDeviceError(verb, subject, state string, xe *XEError) string {
	raw := xe.Stderr + "\n" + xe.Stdout
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "not in an appropriate power state"):
		expected, actual := "", state
		if m := powerStateRe.FindStringSubmatch(raw); m != nil {
			expected, actual = joinStates(m[1]), m[2]
		}
		msg := fmt.Sprintf("Cannot %s %q: the VM is %s", verb, subject, orUnknown(actual))
		if expected != "" {
			msg += fmt.Sprintf(", but it must be %s", expected)
		}
		return msg + "."
	case strings.Contains(lower, "vdi_in_use") || strings.Contains(lower, "vdi is in use") || strings.Contains(lower, "in use by some other operation"):
		return fmt.Sprintf("Cannot %s %q: the disk is in use by a VM or another operation; detach it (and stop the VM) first.", verb, subject)
	case strings.Contains(lower, "the uuid you supplied was invalid") || strings.Contains(lower, "no matching"):
		return fmt.Sprintf("Cannot %s %q: the object does not exist (check the uuid).", verb, subject)
	case strings.Contains(lower, "requires pv drivers") || strings.Contains(lower, "pv drivers to be installed"):
		return fmt.Sprintf("Cannot %s %q: the guest has no Xen PV drivers (guest tools) running, so it cannot hot-plug or hot-unplug devices; stop the VM first, or install the guest tools.", verb, subject)
	case strings.Contains(lower, "device_already_detached") || strings.Contains(lower, "not currently attached") || (strings.Contains(verb, "unplug") && strings.Contains(lower, "already detached")):
		return fmt.Sprintf("Cannot %s %q: the device is already unplugged.", verb, subject)
	case strings.HasPrefix(verb, "plug ") && strings.Contains(verb, "network card") && (strings.Contains(lower, "device_already_attached") || strings.Contains(lower, "already attached")):
		return fmt.Sprintf("Cannot %s %q: the network card is already plugged in.", verb, subject)
	case strings.Contains(verb, "plug disk") && !strings.Contains(verb, "unplug") && (strings.Contains(lower, "device_already_attached") || strings.Contains(lower, "already attached")):
		return fmt.Sprintf("Cannot %s %q: the disk is already plugged in (a VM that is running had it attached when it started).", verb, subject)
	case strings.Contains(lower, "device_already_attached") || strings.Contains(lower, "already attached") || strings.Contains(lower, "already in use"):
		return fmt.Sprintf("Cannot %s %q: that device position is already in use on the VM; pick another position.", verb, subject)
	case strings.Contains(lower, "operation_not_allowed") || strings.Contains(lower, "not allowed"):
		return fmt.Sprintf("Cannot %s %q: not allowed in the current state (%s). %s", verb, subject, orUnknown(state), strings.TrimSpace(xe.Stderr))
	}
	return fmt.Sprintf("Failed to %s %q: %s", verb, subject, strings.TrimSpace(xe.Stderr+" "+xe.Stdout))
}
