// Package config handles loading and validation of the agent configuration.
// Configuration is layered: defaults → config file → environment variables.
// Environment variables use the prefix PLUSCLOUDS_AGENT_ and dot-separated
// keys map to underscores (e.g. nats.url → PLUSCLOUDS_AGENT_NATS_URL).
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// AgentVersion is the build version. It is stamped at build time with
// -ldflags "-X .../internal/config.AgentVersion=<version>" (see the Makefile);
// a plain `go build` reports "dev".
var AgentVersion = "dev"

// Config is the top-level configuration structure for the agent.
type Config struct {
	NATS        NATSConfig        `mapstructure:"nats"`
	Agent       AgentConfig       `mapstructure:"agent"`
	ISO         ISOConfig         `mapstructure:"iso"`
	Log         LogConfig         `mapstructure:"log"`
	Autoheal    AutohealConfig    `mapstructure:"autoheal"`
	XenServer   XenServerConfig   `mapstructure:"xenserver"`
	OpenVSwitch OpenVSwitchConfig `mapstructure:"openvswitch"`
	NFSMount    NFSMountConfig    `mapstructure:"nfsmount"`
	HTTP        HTTPConfig        `mapstructure:"http"`
}

// HTTPConfig holds the optional HTTPS command endpoint settings.
type HTTPConfig struct {
	// Enabled starts the HTTPS server (POST /v1/command, GET /healthz).
	Enabled bool `mapstructure:"enabled"`
	// Listen is the bind address, e.g. ":8443" or "127.0.0.1:8443".
	Listen string `mapstructure:"listen"`
	// CertFile and KeyFile are the PEM TLS certificate and private key paths.
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
	// SelfSigned generates and persists a self-signed certificate at
	// CertFile/KeyFile when they do not exist (and renews it before expiry).
	// Operator-supplied certificates are never overwritten.
	SelfSigned bool `mapstructure:"self_signed"`
	// APIKey is the bearer token HTTPS callers must present. When empty the
	// agent falls back to nats.api_key (or the ISO agent_api_key).
	APIKey string `mapstructure:"api_key"`
	// Hosts are extra DNS names / IPs added to the self-signed certificate SANs.
	Hosts []string `mapstructure:"hosts"`
}

// Connection type constants for NATSConfig.ConnectionType.
const (
	ConnectionTypeNATS      = "nats"
	ConnectionTypeWebSocket = "websocket"
)

// NATSConfig holds NATS connection settings.
type NATSConfig struct {
	// Enabled turns the NATS transport on or off (default true). Set false for
	// HTTPS-only operation: no NATS connection is made and the agent no longer
	// pushes heartbeat/telemetry/events — the platform must poll over HTTPS.
	// Requires http.enabled: true.
	Enabled bool `mapstructure:"enabled"`
	// ConnectionType selects the transport: "nats" (TCP, default) or "websocket".
	// Use "websocket" to connect through firewalls/proxies without TLS certificates.
	ConnectionType string `mapstructure:"connection_type"`
	// URL is the NATS TCP server address, used when connection_type is "nats".
	// Example: nats://nats.plusclouds.com:4222
	URL string `mapstructure:"url"`
	// WebSocketURL is the NATS WebSocket address, used when connection_type is "websocket".
	// Use ws:// for plain WebSocket (no TLS). Example: ws://nats.plusclouds.com:8080
	WebSocketURL string `mapstructure:"websocket_url"`
	// AgentUUID is the fallback VM UUID used as the NATS username when the ISO
	// config drive is not present. The ISO virtual_machine_id always takes precedence.
	// Can also be set via PLUSCLOUDS_AGENT_NATS_AGENT_UUID environment variable.
	AgentUUID string `mapstructure:"agent_uuid"`
	// APIKey is the fallback NATS authentication token when the ISO config drive
	// does not carry an agent_api_key. The ISO value always takes precedence.
	// Can also be set via PLUSCLOUDS_AGENT_NATS_API_KEY environment variable.
	APIKey string `mapstructure:"api_key"`
	// MaxReconnects controls how many times the client retries on disconnect.
	// -1 means unlimited.
	MaxReconnects int `mapstructure:"max_reconnects"`
	// ReconnectWait is the delay between reconnect attempts.
	ReconnectWait time.Duration `mapstructure:"reconnect_wait"`
}

// ActiveURL returns the server URL for the configured connection type.
func (c NATSConfig) ActiveURL() string {
	if c.ConnectionType == ConnectionTypeWebSocket {
		return c.WebSocketURL
	}
	return c.URL
}

// AgentConfig holds agent behaviour settings.
type AgentConfig struct {
	// HeartbeatInterval is how often the agent publishes a heartbeat event.
	HeartbeatInterval time.Duration `mapstructure:"heartbeat_interval"`
	// TelemetryInterval is how often the agent publishes a telemetry event.
	TelemetryInterval time.Duration `mapstructure:"telemetry_interval"`
	// AllowedOperations is the set of operation names the dispatcher will execute.
	// Any operation not listed is rejected without execution.
	AllowedOperations []string `mapstructure:"allowed_operations"`
	// AllowedCommands is the allowlist of binary paths the exec operation may run.
	// Only consulted when "exec" appears in AllowedOperations.
	AllowedCommands []string `mapstructure:"allowed_commands"`
	// DryRun disables all subprocess execution (xe, ovs-vsctl, ovs-ofctl,
	// mount, apt-get, the generic exec operation, vm.reboot/shutdown, etc.):
	// commands are logged instead of run. It does not cover services.* —
	// those go through systemd D-Bus, not a subprocess.
	DryRun bool `mapstructure:"dry_run"`
	// MaxJobs is how many background (params.async) jobs may run at once.
	MaxJobs int `mapstructure:"max_jobs"`
}

// ISOConfig holds ISO/config-drive settings.
type ISOConfig struct {
	// MountPath is the filesystem path where the config drive ISO is mounted.
	MountPath string `mapstructure:"mount_path"`
	// FallbackEnv enables falling back to environment variables when ISO files
	// are absent. Useful for development and testing without a real ISO.
	FallbackEnv bool `mapstructure:"fallback_env"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	// Level is the minimum log level: debug, info, warn, error.
	Level string `mapstructure:"level"`
	// Format controls log output to stdout: json or console.
	Format string `mapstructure:"format"`
	// File is an optional path to write logs to in addition to stdout.
	// Always written in JSON format regardless of the Format setting.
	// Leave empty to disable file logging.
	File string `mapstructure:"file"`
}

// AutohealConfig holds automatic service recovery settings.
type AutohealConfig struct {
	// Enabled activates automatic restart of failed services.
	Enabled bool `mapstructure:"enabled"`
	// RestartDelay is how long to wait before restarting a failed service.
	RestartDelay time.Duration `mapstructure:"restart_delay"`
}

// XenServerConfig holds settings for managing the XenServer/XCP-ng
// hypervisor this agent runs on as Dom0.
type XenServerConfig struct {
	// XEBinary is the path to the xe CLI binary.
	XEBinary string `mapstructure:"xe_binary"`
	// InventoryPath is the xensource-inventory file used to resolve this
	// host's own uuid (INSTALLATION_UUID) for host-scoped operations.
	InventoryPath string `mapstructure:"inventory_path"`
	// XAPISocketPath is the local xapi Unix domain socket used for raw VDI
	// export (xenserver.vdi.export), which has no xe subprocess equivalent.
	XAPISocketPath string `mapstructure:"xapi_socket_path"`
	// IPMIBinary is the path to the ipmitool binary, used for
	// xenserver.ipmi.sensors and the periodic IPMI telemetry push.
	IPMIBinary string `mapstructure:"ipmi_binary"`
	// EventsEnabled starts a background loop that long-polls XAPI's own
	// event bus (event.from) and publishes each object-change event to the
	// platform. Replaces the events.py sidecar script.
	EventsEnabled bool `mapstructure:"events_enabled"`
	// EventsPollInterval is the delay between successive event.from calls
	// (each of which itself long-polls XAPI for up to ~30s for new events).
	EventsPollInterval time.Duration `mapstructure:"events_poll_interval"`
	// IPMIEnabled starts a background loop that periodically reads IPMI
	// sensor data and publishes it as a "ipmi" event. Replaces ipmi.py.
	IPMIEnabled bool `mapstructure:"ipmi_enabled"`
	// IPMIInterval is how often IPMI sensors are read when IPMIEnabled.
	IPMIInterval time.Duration `mapstructure:"ipmi_interval"`
	// RRDEnabled starts merging XAPI RRD-derived per-VM utilisation metrics
	// (CPU/memory/disk/network actually observed inside each guest, sourced
	// from XAPI's own performance monitoring) into the periodic telemetry
	// push, as a "vms" key. Replaces rrd.py.
	RRDEnabled bool `mapstructure:"rrd_enabled"`
}

// OpenVSwitchConfig holds settings for managing the host's Open vSwitch
// backend (VXLAN tunnels, raw flow rules).
type OpenVSwitchConfig struct {
	// VSCtlBinary is the path to the ovs-vsctl binary.
	VSCtlBinary string `mapstructure:"vsctl_binary"`
	// OfctlBinary is the path to the ovs-ofctl binary.
	OfctlBinary string `mapstructure:"ofctl_binary"`
}

// NFSMountConfig holds settings for mounting NFS shares used to stage VM
// import/export files.
type NFSMountConfig struct {
	// MountBinary is the path to the mount binary.
	MountBinary string `mapstructure:"mount_binary"`
	// UmountBinary is the path to the umount binary.
	UmountBinary string `mapstructure:"umount_binary"`
}

// Load reads the configuration from the given file path, then overlays
// environment variables (prefix PLUSCLOUDS_AGENT_), and finally applies
// built-in defaults for any missing values.
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	setDefaults(v)

	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		v.SetConfigName("agent")
		v.SetConfigType("yaml")
		v.AddConfigPath("/etc/plusclouds")
		v.AddConfigPath("$HOME/.plusclouds")
		v.AddConfigPath("./configs")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
	}

	v.SetEnvPrefix("PLUSCLOUDS_AGENT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("nats.enabled", true)
	v.SetDefault("nats.connection_type", ConnectionTypeNATS)
	v.SetDefault("nats.url", "nats://localhost:4222")
	v.SetDefault("nats.websocket_url", "ws://localhost:8080")
	v.SetDefault("nats.agent_uuid", "")
	v.SetDefault("nats.api_key", "")
	v.SetDefault("nats.max_reconnects", -1)
	v.SetDefault("nats.reconnect_wait", 5*time.Second)

	v.SetDefault("agent.heartbeat_interval", 30*time.Second)
	v.SetDefault("agent.telemetry_interval", 30*time.Second)
	v.SetDefault("agent.allowed_operations", []string{
		"agent.allowed_operations",
		"services.list",
		"services.get",
		"services.start",
		"services.stop",
		"services.restart",
		"services.reload",
		"services.enable",
		"services.disable",
		"system.info",
		"system.metrics",
		"system.cpu",
		"system.memory",
		"system.disk",
		"system.network",
		"system.update",
		"telemetry.set_interval",
		"vm.reboot",
		"vm.shutdown",
		// Read-only XenServer/OVS/NFS inventory ops — safe to enable by
		// default, same treatment as system.*. State-changing ops in these
		// areas (xenserver.vm.start, openvswitch.flow.add, nfsmount.mount,
		// etc.) are deliberately absent here, same treatment as "exec".
		"xenserver.pool.info",
		"xenserver.host.list",
		"xenserver.host.info",
		"xenserver.vm.list",
		"xenserver.vm.get",
		"xenserver.vm.snapshot_list",
		"xenserver.sr.list",
		"xenserver.sr.get",
		"xenserver.network.list",
		"xenserver.vif.list",
		"xenserver.vm.disk_list",
		"xenserver.vm.param_get",
		"xenserver.vm.param_list",
		"xenserver.vm.console_list",
		"xenserver.vm.cd_list",
		"xenserver.vdi.param_list",
		"xenserver.vbd.param_list",
		"xenserver.vif.param_list",
		"xenserver.network.param_list",
		"xenserver.vlan.list",
		"xenserver.vlan.param_list",
		"xenserver.pif.list",
		"xenserver.pif.param_list",
		"xenserver.pif.param_get",
		"xenserver.pbd.list",
		"xenserver.pbd.param_list",
		"xenserver.task.list",
		"xenserver.sr.param_list",
		"xenserver.sr.param_get",
		"xenserver.host.param_list",
		"xenserver.vdi.list",
		"xenserver.ipmi.sensors",
		"xenserver.metrics.rrd",
		"openvswitch.bridge.list",
		"openvswitch.port.list",
		"openvswitch.flow.list",
		"nfsmount.list",
		"nfsmount.list_files",
		"agent.job.status",
		"agent.job.list",
	})
	v.SetDefault("agent.allowed_commands", []string{})
	v.SetDefault("agent.dry_run", false)
	v.SetDefault("agent.max_jobs", 2)

	v.SetDefault("iso.mount_path", "/media/plusclouds-config")
	v.SetDefault("iso.fallback_env", true)

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.file", "")

	v.SetDefault("autoheal.enabled", true)
	v.SetDefault("autoheal.restart_delay", 10*time.Second)

	v.SetDefault("xenserver.xe_binary", "/usr/bin/xe")
	v.SetDefault("xenserver.inventory_path", "/etc/xensource-inventory")
	v.SetDefault("xenserver.xapi_socket_path", "/var/lib/xcp/xapi")
	v.SetDefault("xenserver.ipmi_binary", "/usr/bin/ipmitool")
	v.SetDefault("xenserver.events_enabled", false)
	v.SetDefault("xenserver.events_poll_interval", 10*time.Second)
	v.SetDefault("xenserver.ipmi_enabled", false)
	v.SetDefault("xenserver.ipmi_interval", 60*time.Second)
	v.SetDefault("xenserver.rrd_enabled", false)

	v.SetDefault("openvswitch.vsctl_binary", "/usr/bin/ovs-vsctl")
	v.SetDefault("openvswitch.ofctl_binary", "/usr/bin/ovs-ofctl")

	v.SetDefault("nfsmount.mount_binary", "/usr/bin/mount")
	v.SetDefault("nfsmount.umount_binary", "/usr/bin/umount")

	v.SetDefault("http.enabled", false)
	v.SetDefault("http.listen", ":8443")
	v.SetDefault("http.cert_file", "/var/lib/plusclouds/tls/agent.crt")
	v.SetDefault("http.key_file", "/var/lib/plusclouds/tls/agent.key")
	v.SetDefault("http.self_signed", true)
	v.SetDefault("http.api_key", "")
	v.SetDefault("http.hosts", []string{})
}
