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
		sample      = flag.Duration("sample-interval", 30*time.Second, "how often to sample per-hop liveness and latency")
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
	authSvc := auth.New(st)

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
	})

	w := watcher.New(watcher.Config{
		Service:        svc,
		Store:          st,
		Logger:         logger,
		Interval:       *interval,
		SampleInterval: *sample,
	})
	go w.Run(ctx)

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
	return nil
}
