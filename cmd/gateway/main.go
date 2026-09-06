// Command gateway is the community-teslafleet service: it ingests Tesla Fleet
// Telemetry (via fleet-telemetry's MQTT output) and exposes it as a local Fleet
// API (for TeslaMate to poll, free) and as Home Assistant MQTT auto-discovery.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/fleetapi"
	"github.com/LasseLegarth/community-teslafleet/internal/hadiscovery"
	"github.com/LasseLegarth/community-teslafleet/internal/ingest"
	"github.com/LasseLegarth/community-teslafleet/internal/onboard"
	"github.com/LasseLegarth/community-teslafleet/internal/recorder"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
	"github.com/LasseLegarth/community-teslafleet/internal/streamcfg"
	"github.com/LasseLegarth/community-teslafleet/internal/supervisor"
	"github.com/LasseLegarth/community-teslafleet/internal/vehicledata"
	"github.com/LasseLegarth/community-teslafleet/internal/wss"
)

func main() {
	cfgPath := flag.String("config", envOr("TGW_CONFIG", "/config/config.yaml"), "path to config.yaml")
	flag.Parse()

	// HA add-on: auto-configure the MQTT broker from the Supervisor (no-op standalone).
	config.DetectSupervisorMQTT()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	log := newLogger(cfg.LogLevel)
	log.Info("starting community-teslafleet",
		"vehicles", len(cfg.Vehicles), "fleetapi", cfg.FleetAPI.Enabled, "ha", cfg.HA.Enabled)
	if len(cfg.Vehicles) == 0 {
		log.Info("no vehicles configured — auto-discovering every car seen on the stream")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Goroutines whose LAST action happens at shutdown. Registered here, before any
	// other defer, so the wait runs last — after the deferred Stop()/Close() calls.
	var bg sync.WaitGroup
	defer waitBackground(&bg, log)

	// The state snapshotter runs on its OWN context, cancelled only after ingest
	// has been stopped. Sharing the signal context raced: SIGTERM triggered the
	// final Save at the same instant the consumer was still applying its last
	// message, so an accepted update could be missing from disk after a clean
	// shutdown. Deferred here, so it runs immediately before waitBackground.
	persistCtx, cancelPersist := context.WithCancel(context.Background())
	defer cancelPersist()

	// All-in-one mode (HA add-on): run fleet-telemetry — and the vehicle-command
	// proxy when commands are enabled — as supervised child processes, and ingest
	// from the dispatcher they bind locally. Standalone leaves this off and runs
	// those as separate docker-compose services.
	var sup *supervisor.Supervisor
	if cfg.Stream.Embedded {
		sup = supervisor.New(log)
		// fleet-telemetry is mTLS-only and won't start without a cert. Use the
		// configured cert (port-forward + Let's Encrypt) or generate a self-signed one
		// (Cloudflare-Tunnel mode, where the tunnel provides the car-facing TLS).
		cert, key := cfg.Stream.TLSCert, cfg.Stream.TLSKey
		if cert == "" || key == "" {
			dir := filepath.Dir(cfg.Stream.FleetTelemetryConfig)
			cert = filepath.Join(dir, "certs", "self-signed-cert.pem")
			key = filepath.Join(dir, "certs", "self-signed-key.pem")
			if gen, err := streamcfg.EnsureSelfSigned(cert, key, ""); err != nil {
				log.Error("could not generate self-signed TLS cert", "err", err)
			} else if gen {
				log.Info("generated self-signed TLS cert for fleet-telemetry (tunnel mode)", "cert", cert)
			}
		}
		// Generate the fleet-telemetry server config from our config every boot, so
		// port/namespace/TLS changes take effect and the binary always has a valid file.
		srvCfg := streamcfg.Build(cfg.Ingest.Namespace, cfg.Stream.ZMQBind,
			cert, key, cfg.Stream.TelemetryPort)
		if err := streamcfg.Write(cfg.Stream.FleetTelemetryConfig, srvCfg); err != nil {
			log.Error("could not write fleet-telemetry config", "path", cfg.Stream.FleetTelemetryConfig, "err", err)
		} else {
			certMode := "self-signed (tunnel)"
			if cfg.Stream.TLSCert != "" && cfg.Stream.TLSKey != "" {
				certMode = "provided cert (port-forward)"
			}
			log.Info("generated fleet-telemetry config",
				"path", cfg.Stream.FleetTelemetryConfig, "port", cfg.Stream.TelemetryPort, "tls", certMode)
		}
		sup.Add(supervisor.Process{
			Name: "fleet-telemetry",
			Path: cfg.Stream.FleetTelemetryBin,
			Args: []string{"-config", cfg.Stream.FleetTelemetryConfig},
		})
		// Ingest from the local dispatcher the embedded fleet-telemetry binds.
		cfg.Ingest.ZMQAddr = localZMQ(cfg.Stream.ZMQBind)
		sup.Start(ctx)
		log.Info("embedded stream mode: supervising upstream Tesla processes",
			"count", sup.Len(), "ingest", cfg.Ingest.ZMQAddr)
	}

	vins := make([]string, 0, len(cfg.Vehicles))
	for _, v := range cfg.Vehicles {
		vins = append(vins, v.VIN)
	}
	st := store.New(vins...)

	// Restore last-known field values from disk so an asleep/away car's sensors
	// (battery, charge-limit, plugged_in, …) survive a restart instead of reading
	// "unknown" until the car next wakes. Then keep the snapshot fresh in the
	// background.
	if p := cfg.State.SnapshotPath; p != "" {
		if n, err := st.Load(p); err != nil {
			log.Warn("state snapshot load failed", "path", p, "err", err)
		} else if n > 0 {
			log.Info("state snapshot restored", "path", p, "vehicles", n)
		}
		bg.Add(1)
		go func() {
			defer bg.Done()
			st.Persist(persistCtx, p, 30*time.Second, log)
		}()
	}

	// Load per-VIN templates (captured vehicle_data). Missing template → skeleton.
	tmpls := map[string]*vehicledata.Template{}
	for _, v := range cfg.Vehicles {
		t, err := vehicledata.LoadTemplate(v.Template)
		if err != nil {
			log.Warn("template", "vin", v.VIN, "err", err)
		}
		tmpls[v.VIN] = t
	}

	// Optional JSONL recorder (debug value changes over time, e.g. a whole drive).
	var rec *recorder.Recorder
	if cfg.Recording.Enabled {
		r, err := recorder.New(cfg.Recording.Path, cfg.Recording.MaxMB, log)
		if err != nil {
			log.Error("recorder init failed", "err", err)
		} else {
			rec = r
			defer rec.Close()
		}
	}

	// Telemetry consumer (brokerless ZMQ from fleet-telemetry).
	consumer := ingest.NewConsumer(cfg.Ingest, st, rec, log)
	if err := consumer.Start(); err != nil {
		log.Error("zmq ingest start failed", "err", err)
		os.Exit(1)
	}
	defer consumer.Stop()

	// Command relay (optional): HA buttons/switches → signed Tesla commands.
	var relay *commands.Relay
	if cfg.Commands.Enabled {
		relay = commands.NewRelay(cfg.Commands, vins, st, log)
		log.Info("command relay enabled", "proxy", cfg.Commands.ProxyURL)
		// Auto-name each vehicle from the Fleet API (display_name) so the HA device
		// is "Tesla - <name>" instead of the VIN. Only when not explicitly named.
		for i := range cfg.Vehicles {
			if cfg.Vehicles[i].DisplayName != cfg.Vehicles[i].VIN {
				continue
			}
			if dn, err := relay.VehicleDisplayName(cfg.Vehicles[i].VIN); err == nil && dn != "" {
				cfg.Vehicles[i].DisplayName = "Tesla - " + dn
				log.Info("auto-named vehicle", "name", cfg.Vehicles[i].DisplayName)
			} else if err != nil {
				log.Warn("could not fetch vehicle display name; keeping default", "err", err)
			}
		}
	}

	// Onboarding wizard (optional) — its own listener, off the auth-less Fleet API port.
	if cfg.Onboard.Enabled {
		obOpts := onboard.Options{
			DataDir:    cfg.Onboard.DataDir,
			Password:   cfg.Onboard.Password,
			AuthHost:   cfg.Commands.AuthHost,
			AuthPath:   cfg.Commands.AuthPath,
			FleetAPI:   cfg.Commands.FleetAPIURL,
			ClientID:   cfg.Commands.ClientID,
			ProxyURL:   cfg.Commands.ProxyURL,
			EnrollFile: cfg.Commands.EnrollFile,
			TokenCache: cfg.Commands.TokenCache,
		}
		if ob, err := onboard.NewServer(obOpts, log); err != nil {
			log.Error("onboard init failed", "err", err)
		} else {
			osrv := &http.Server{Addr: cfg.Onboard.Listen, Handler: ob.Handler(), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				log.Info("onboarding wizard listening", "addr", cfg.Onboard.Listen)
				if err := osrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Error("onboard server error", "err", err)
				}
			}()
			// Unauthenticated public endpoint serving the partner public key, so the
			// user can route their domain's /.well-known here (Tesla fetches it).
			if cfg.Onboard.WellKnownListen != "" {
				wksrv := &http.Server{Addr: cfg.Onboard.WellKnownListen, Handler: ob.WellKnownHandler(), ReadHeaderTimeout: 10 * time.Second}
				go func() {
					log.Info("well-known public-key endpoint listening", "addr", cfg.Onboard.WellKnownListen, "path", onboard.WellKnownPath)
					if err := wksrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						log.Error("well-known server error", "err", err)
					}
				}()
			}
		}
	}

	// Home Assistant publisher (optional).
	var publisher *hadiscovery.Publisher
	if cfg.HA.Enabled {
		publisher = hadiscovery.NewPublisher(&cfg, st, relay, log)
		if err := publisher.Start(); err != nil {
			log.Error("ha publisher start failed", "err", err)
		} else {
			defer publisher.Stop()
		}
	}

	// Fleet API emulator + legacy WSS streaming (both TeslaMate-facing, same port).
	var srv *http.Server
	if cfg.FleetAPI.Enabled {
		api := fleetapi.NewServer(st, &cfg, tmpls, relay, cfg.Commands.EnrollFile, log)
		api.SetIngestHealth(consumer) // /healthz reports the real ingest link
		router := api.Routes()
		wssSrv := wss.NewServer(st, &cfg, log)
		router.Handle("/streaming/*", wssSrv.Handler())
		router.Handle("/streaming", wssSrv.Handler())
		srv = &http.Server{
			Addr:    cfg.HTTP.Listen,
			Handler: router,
			// WriteTimeout is intentionally left unset: the WSS handler serves
			// long-lived streaming connections.
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		go func() {
			log.Info("fleet api + wss listening", "addr", cfg.HTTP.Listen)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("fleet api server error", "err", err)
			}
		}()
	}

	<-ctx.Done()
	log.Info("shutting down")
	if srv != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
	if sup != nil {
		log.Info("waiting for supervised processes to stop")
		sup.Wait()
	}
}

// waitBackground waits for goroutines that flush something on the way out —
// today just the state snapshot that store.Persist writes when ctx is cancelled.
// It was started with a bare `go` and never waited on, so main returned and the
// process exited while that save was still in flight: the "saved on shutdown"
// guarantee was a race, and losing it silently costs up to one persist interval
// of field values, which is what makes an asleep car's sensors read unknown after
// a restart.
//
// Bounded, because a wedged goroutine must not be able to stop the process from
// exiting — a container runtime would just SIGKILL it, which is strictly worse.
func waitBackground(bg *sync.WaitGroup, log *slog.Logger) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		bg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Warn("background tasks did not finish within 5s — state snapshot may be stale")
	}
}

// localZMQ turns a dispatcher bind address (e.g. tcp://0.0.0.0:5284) into the
// loopback address the gateway connects to for an embedded fleet-telemetry.
func localZMQ(bind string) string {
	if i := strings.LastIndex(bind, ":"); i >= 0 {
		return "tcp://127.0.0.1" + bind[i:]
	}
	return "tcp://127.0.0.1:5284"
}

func newLogger(level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
