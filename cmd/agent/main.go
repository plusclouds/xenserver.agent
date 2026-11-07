// Command plusclouds-agent is the PlusClouds VM agent daemon.
// It collects system metrics, manages services, and communicates with the
// PlusClouds platform exclusively via NATS. Supports Linux and Windows.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/plusclouds/ubuntu-agent/internal/config"
	"github.com/plusclouds/ubuntu-agent/internal/dispatcher"
	"github.com/plusclouds/ubuntu-agent/internal/executor"
	"github.com/plusclouds/ubuntu-agent/internal/httpserver"
	"github.com/plusclouds/ubuntu-agent/internal/jobs"
	"github.com/plusclouds/ubuntu-agent/internal/modules/nfsmount"
	"github.com/plusclouds/ubuntu-agent/internal/modules/openvswitch"
	"github.com/plusclouds/ubuntu-agent/internal/modules/system"
	"github.com/plusclouds/ubuntu-agent/internal/modules/xenserver"
	natsclient "github.com/plusclouds/ubuntu-agent/internal/nats"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
	"github.com/plusclouds/ubuntu-agent/internal/publisher"
	"github.com/plusclouds/ubuntu-agent/pkg/isoconfig"
)

var cfgFile string

func main() {
	root := &cobra.Command{
		Use:     "plusclouds-agent",
		Short:   "PlusClouds VM Agent",
		Version: config.AgentVersion,
		RunE:    run,
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "",
		"Path to config file (default: /etc/plusclouds/agent.yaml)")

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(_ *cobra.Command, _ []string) error {
	// ------------------------------------------------------------------ //
	// 1. Load configuration
	// ------------------------------------------------------------------ //
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// ------------------------------------------------------------------ //
	// 2. Initialise logger
	// ------------------------------------------------------------------ //
	logger, err := buildLogger(cfg)
	if err != nil {
		return fmt.Errorf("building logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	logger.Info("PlusClouds agent starting",
		zap.String("version", config.AgentVersion),
		zap.String("config_file", cfgFile),
	)

	// ------------------------------------------------------------------ //
	// 3. Resolve identity — config file is primary; ISO overrides if mounted.
	// ------------------------------------------------------------------ //
	agentUUID := cfg.NATS.AgentUUID
	agentAPIKey := cfg.NATS.APIKey

	isoReader := isoconfig.NewReader(cfg.ISO.MountPath)
	iso, err := isoReader.Read()
	if err != nil {
		logger.Debug("ISO config drive not available (expected in production)",
			zap.String("mount_path", cfg.ISO.MountPath),
			zap.Error(err),
		)
		iso = &isoconfig.ISOMetadata{}
	} else if iso.VMID() != "" {
		logger.Debug("ISO config drive found, overriding agent identity",
			zap.String("vm_id", iso.VMID()),
		)
		agentUUID = iso.VMID()
		agentAPIKey = iso.AgentAPIKey()
	}

	if agentUUID == "" {
		logger.Warn("agent_uuid is not set — NATS auth will fail; set nats.agent_uuid in agent.yaml")
	}
	if agentAPIKey == "" {
		logger.Warn("api_key is not set — NATS auth will fail; set nats.api_key in agent.yaml")
	}

	logger.Info("agent identity resolved", zap.String("agent_uuid", agentUUID))

	// ------------------------------------------------------------------ //
	// 4. Initialise platform-specific service manager
	// ------------------------------------------------------------------ //
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svcMgr, svcCleanup := newServiceManager(ctx, logger)
	defer svcCleanup()

	// ------------------------------------------------------------------ //
	// 5. Initialise remaining modules
	// ------------------------------------------------------------------ //
	if cfg.Agent.DryRun {
		logger.Warn("agent is running in DRY-RUN mode — commands will be logged, not executed")
	}

	sysMod := system.New(iso)
	exec := executor.New(logger, cfg.Agent.DryRun)
	xenMod := xenserver.New(exec, cfg.XenServer.XEBinary, cfg.XenServer.InventoryPath, cfg.XenServer.XAPISocketPath, cfg.XenServer.IPMIBinary, logger)
	xenMod.SetISOBinary(cfg.XenServer.ISOBinary)
	ovsMod := openvswitch.New(exec, cfg.OpenVSwitch.VSCtlBinary, cfg.OpenVSwitch.OfctlBinary, cfg.XenServer.XEBinary, logger)
	nfsMod := nfsmount.New(exec, cfg.NFSMount.MountBinary, cfg.NFSMount.UmountBinary, logger)
	logger.Info("modules initialised",
		zap.Int("allowed_operations", len(cfg.Agent.AllowedOperations)),
		zap.Int("allowed_commands", len(cfg.Agent.AllowedCommands)),
		zap.Duration("telemetry_interval", cfg.Agent.TelemetryInterval),
		zap.Duration("heartbeat_interval", cfg.Agent.HeartbeatInterval),
	)

	// ------------------------------------------------------------------ //
	// 6. Connect to NATS
	// ------------------------------------------------------------------ //
	if !cfg.NATS.Enabled && !cfg.HTTP.Enabled {
		return fmt.Errorf("both nats.enabled and http.enabled are false — no transport to receive commands")
	}
	var nc *natsclient.Client // nil when NATS is disabled (HTTPS-only)
	if cfg.NATS.Enabled {
		nc, err = natsclient.Connect(cfg.NATS, agentUUID, agentAPIKey, logger)
		if err != nil {
			return fmt.Errorf("NATS connection failed: %w", err)
		}
		defer nc.Drain()
	} else {
		logger.Info("NATS disabled — running HTTPS-only")
	}

	// ------------------------------------------------------------------ //
	// 7. Create publisher and dispatcher
	// ------------------------------------------------------------------ //
	pub := publisher.New(nc, sysMod, xenMod, agentUUID, cfg.Agent, cfg.XenServer, logger)

	disp := dispatcher.New(
		sysMod, svcMgr, exec, xenMod, ovsMod, nfsMod, pub,
		agentUUID,
		cfg.Agent.AllowedOperations,
		cfg.Agent.AllowedCommands,
		logger,
	)

	jobMgr := jobs.New(logger, cfg.Agent.MaxJobs, 100)
	defer jobMgr.Shutdown()
	disp.SetDryRun(cfg.Agent.DryRun)
	disp.SetCallbackAllowedURLs(cfg.Agent.CallbackAllowedURLs)
	disp.SetJobs(jobMgr)

	// ------------------------------------------------------------------ //
	// 8. Subscribe to cmd subject
	// ------------------------------------------------------------------ //
	if nc != nil {
		if err := nc.Subscribe(func(env protocol.Envelope) {
			result := disp.Dispatch(ctx, env)

			if env.ReplyTo != "" {
				// Synchronous caller is blocking on this inbox — reply directly.
				if err := nc.PublishToSubject(env.ReplyTo, result); err != nil {
					logger.Error("could not publish sync result",
						zap.String("command_id", env.ID),
						zap.String("reply_to", env.ReplyTo),
						zap.Error(err),
					)
				}
			} else {
				// Async path — result goes to the evt subject as usual.
				if err := nc.Publish(result); err != nil {
					logger.Error("could not publish result to evt subject",
						zap.String("command_id", env.ID),
						zap.Error(err),
					)
				}
			}
		}); err != nil {
			return fmt.Errorf("subscribing to NATS cmd subject: %w", err)
		}
	}

	// ------------------------------------------------------------------ //
	// 8b. Optional HTTPS command endpoint (bearer token = agent API key)
	// ------------------------------------------------------------------ //
	if cfg.HTTP.Enabled {
		httpToken := cfg.HTTP.APIKey
		if httpToken == "" {
			httpToken = agentAPIKey
		}
		httpSrv, err := httpserver.New(cfg.HTTP, disp, httpToken, agentUUID, logger)
		if err != nil {
			return fmt.Errorf("HTTPS server: %w", err)
		}
		if err := httpSrv.Start(); err != nil {
			return fmt.Errorf("starting HTTPS server: %w", err)
		}
		defer func() {
			sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer scancel()
			_ = httpSrv.Shutdown(sctx)
		}()
	}

	// ------------------------------------------------------------------ //
	// 9. Start heartbeat and telemetry publisher
	// ------------------------------------------------------------------ //
	pub.Start(ctx)

	if nc != nil {
		logger.Info("agent started",
			zap.String("nats_url", cfg.NATS.ActiveURL()),
			zap.String("cmd_subject", nc.CmdSubject()),
			zap.String("evt_subject", nc.EvtSubject()),
		)
	} else {
		logger.Info("agent started (HTTPS-only)", zap.String("listen", cfg.HTTP.Listen))
	}

	// ------------------------------------------------------------------ //
	// 10. Wait for OS signal
	// ------------------------------------------------------------------ //
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("received shutdown signal", zap.String("signal", sig.String()))

	logger.Info("initiating graceful shutdown")
	cancel()
	logger.Info("agent stopped cleanly")
	return nil
}

func buildLogger(cfg *config.Config) (*zap.Logger, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		level = zapcore.InfoLevel
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	var stdoutEnc zapcore.Encoder
	if cfg.Log.Format == "console" {
		consoleEncCfg := zap.NewDevelopmentEncoderConfig()
		consoleEncCfg.EncodeTime = zapcore.ISO8601TimeEncoder
		stdoutEnc = zapcore.NewConsoleEncoder(consoleEncCfg)
	} else {
		stdoutEnc = zapcore.NewJSONEncoder(encCfg)
	}
	stdoutCore := zapcore.NewCore(stdoutEnc, zapcore.AddSync(os.Stdout), level)
	cores := []zapcore.Core{stdoutCore}

	if cfg.Log.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.Log.File), 0755); err != nil {
			return nil, fmt.Errorf("creating log directory: %w", err)
		}
		f, err := os.OpenFile(cfg.Log.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("opening log file %s: %w", cfg.Log.File, err)
		}
		fileCore := zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(f), level)
		cores = append(cores, fileCore)
	}

	return zap.New(zapcore.NewTee(cores...), zap.AddCaller()), nil
}
