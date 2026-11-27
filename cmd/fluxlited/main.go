// Command fluxlited is the fluxlite control plane: an HTTP panel that manages
// multi-hop relay chains across SSH-reachable nodes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kukumi1/fluxlite/internal/api"
	"github.com/kukumi1/fluxlite/internal/applier"
	"github.com/kukumi1/fluxlite/internal/auth"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/komari"
	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"
	"github.com/kukumi1/fluxlite/internal/verifier"
	"github.com/kukumi1/fluxlite/internal/watcher"
	"github.com/kukumi1/fluxlite/web"
)

// version is overridden at build time with -ldflags.
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "import-komari" {
		if err := importKomari(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fluxlited import-komari:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fluxlited:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listen      = flag.String("listen", "127.0.0.1:7800", "address to listen on")
		dataDir     = flag.String("data", "/var/lib/fluxlite", "data directory")
		interval    = flag.Duration("reconcile-interval", 5*time.Minute, "how often to reconcile nodes and routes")
		sample      = flag.Duration("sample-interval", 10*time.Second, "how often to sample node metrics and per-hop liveness")
		trafficPoll = flag.Duration("traffic-interval", 20*time.Second, "how often to collect per-route byte counters")
		insecure    = flag.Bool("insecure-cookies", false, "allow session cookies over plain HTTP (development only)")
		genKey      = flag.Bool("genkey", false, "print a fresh master key and exit")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("fluxlite", version)
		return nil
	}
	if *genKey {
		key, err := cryptox.GenerateMasterKey()
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	masterKey, err := cryptox.LoadMasterKey()
	if err != nil {
		if errors.Is(err, cryptox.ErrNoMasterKey) {
			return fmt.Errorf("%w: set %s to a 32-byte hex key (generate one with --genkey)",
				err, cryptox.EnvMasterKey)
		}
		return err
	}
	sealer, err := cryptox.NewSealer(masterKey)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, filepath.Join(*dataDir, "fluxlite.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	dialer := sshx.NewDialer(st, sealer, st, 20*time.Second)
	pool := sshx.NewPool(dialer)
	defer pool.Close()

	realmSource := applier.NewCachedRealmSource(filepath.Join(*dataDir, "realm"))
	ap := applier.New(pool, realmSource)
	vf := verifier.New(pool)
	svc := service.New(st, sealer, pool, ap, vf, realmSource, logger)
	svc.SetTraceTool(applier.NewCachedNextTraceSource(filepath.Join(*dataDir, "nexttrace")))
	svc.SetSpeedTool(applier.NewCachedTaierSource(filepath.Join(*dataDir, "taier")))
	if err := svc.InterruptSpeedJobs(ctx); err != nil {
		logger.Warn("close speed tests left running", "error", err)
	}
	authSvc := auth.New(st)
	hub := probe.New(st, logger)
	site := komari.New(st, hub, filepath.Join(*dataDir, "themes"), !*insecure, logger)
	site.SetReturnRoutes(func(ctx context.Context) (map[int64]any, error) {
		sums, err := svc.ReturnRouteSummaries(ctx)
		out := make(map[int64]any, len(sums))
		for id, sum := range sums {
			out[id] = sum
		}
		return out, err
	})

	static, err := web.Handler()
	if err != nil {
		return fmt.Errorf("prepare frontend: %w", err)
	}

	srv := api.NewServer(api.Config{
		Auth:          authSvc,
		Service:       svc,
		Logger:        logger,
		Static:        static,
		SecureCookies: !*insecure,
		Probe:         hub,
		Site:          site,
	})

	w := watcher.New(watcher.Config{
		Service:         svc,
		Store:           st,
		Logger:          logger,
		Interval:        *interval,
		SampleInterval:  *sample,
		TrafficInterval: *trafficPoll,
		Notify:          srv.NotifyTelegram,
	})
	go w.Run(ctx)
	probeDone := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(probeDone)
	}()
	go srv.RunTelegram(ctx)

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Applying a route uploads realm to several nodes over SSH, so the
		// write timeout has to accommodate a slow chain rather than the usual
		// sub-second API call.
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("fluxlite listening", "addr", *listen, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	// The probe writes the minutes it still holds on the way out.
	<-probeDone
	return nil
}

// importKomari replaces the probe's machines, latency checks, site settings
// and last week of history with a Komari server's, and copies its active
// theme. Komari's files are only read. Run it with the panel stopped.
func importKomari(args []string) error {
	fs := flag.NewFlagSet("import-komari", flag.ContinueOnError)
	dataDir := fs.String("data", "/var/lib/fluxlite", "fluxlite data directory")
	komari := fs.String("komari", "", "Komari data directory, holding komari.db, metrics.db and theme/")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *komari == "" {
		return errors.New("--komari is required")
	}
	ctx := context.Background()
	in, err := probe.ImportKomari(ctx, filepath.Join(*komari, "komari.db"), filepath.Join(*komari, "metrics.db"), time.Now())
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, filepath.Join(*dataDir, "fluxlite.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.ReplaceProbe(ctx, in); err != nil {
		return err
	}
	samples, pings := 0, 0
	for _, v := range in.Samples {
		samples += len(v)
	}
	for _, v := range in.Pings {
		pings += len(v)
	}
	fmt.Printf("imported %d machines, %d latency checks, %d load buckets, %d latency buckets\n", len(in.Clients), len(in.Tasks), samples, pings)
	if theme := in.Settings["theme"]; theme != "" {
		dst := filepath.Join(*dataDir, "themes", theme)
		if err := probe.CopyTheme(filepath.Join(*komari, "theme", theme), dst); err != nil {
			return fmt.Errorf("copy theme %s: %w", theme, err)
		}
		fmt.Println("copied theme", theme, "to", dst)
	}
	if err := probe.CopyFavicon(filepath.Join(*komari, "favicon.ico"), filepath.Join(*dataDir, "themes", "favicon.ico")); err != nil {
		return fmt.Errorf("copy the site icon: %w", err)
	}
	return nil
}
