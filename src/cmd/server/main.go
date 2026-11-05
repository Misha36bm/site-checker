// Command server runs the site-checker API and its background workers.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/api/routes"
	"github.com/Misha36bm/site-checker/src/internal/browser"
	"github.com/Misha36bm/site-checker/src/internal/checker"
	"github.com/Misha36bm/site-checker/src/internal/config"
	"github.com/Misha36bm/site-checker/src/internal/database"
	_ "github.com/Misha36bm/site-checker/src/internal/database/postgres" // registers the driver
	"github.com/Misha36bm/site-checker/src/internal/netguard"
	"github.com/Misha36bm/site-checker/src/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, get config.Getter) error {
	app, err := config.Load(get)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	dbConfig, err := database.LoadConfig(get)
	if err != nil {
		return fmt.Errorf("load database config: %w", err)
	}

	db, err := database.Open(dbConfig, get)
	if err != nil {
		return err
	}
	// Deferred first, so the database closes after the server and workers stop.
	defer func() {
		if err := db.Close(); err != nil {
			slog.Error("close database", "error", err)
		}
	}()

	if err := database.WaitReady(ctx, db, dbConfig.ConnectTimeout); err != nil {
		return err
	}
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	// Deferred calls run in reverse order. Whatever ends this function, workers are
	// told to stop and waited for, then the proxy closes, then the database.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup

	policy := netguard.NewPolicy(app.AllowedCIDRs)
	dialer := &netguard.Dialer{Policy: policy, Resolver: net.DefaultResolver}
	httpChecker := checker.NewHTTPChecker(checker.HTTPConfig{
		Timeout:      app.HTTP.Timeout,
		MaxRedirects: app.HTTP.MaxRedirects,
		MaxBodyBytes: app.HTTP.MaxBodyBytes,
		MaxURLLength: app.MaxURLLength,
		UserAgent:    app.HTTP.UserAgent,
	}, policy, dialer.DialContext)

	screenshots, err := browser.NewStorage(app.Browser.StoragePath)
	if err != nil {
		return err
	}
	chromium := browser.New(browser.Config{
		CDPURL:       app.Browser.CDPURL,
		Timeout:      app.Browser.Timeout,
		SettleDelay:  app.Browser.SettleDelay,
		Width:        int64(app.Browser.ViewportWidth),
		Height:       int64(app.Browser.ViewportHeight),
		MaxRedirects: app.HTTP.MaxRedirects,
		MaxBytes:     app.Browser.MaxBytes,
		Concurrency:  app.Browser.Concurrency,
	}, net.DefaultResolver)

	// The proxy closes after the workers stop, because checks finishing during
	// shutdown still need it. The listener opens here so a failure stops startup.
	proxyListener, err := net.Listen("tcp", app.Browser.ProxyListen)
	if err != nil {
		return fmt.Errorf("listen for browser proxy: %w", err)
	}
	proxy := &http.Server{
		Handler:           netguard.NewProxy(policy, dialer.DialContext, app.Browser.Timeout),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
	}
	defer proxy.Close()
	go func() {
		if err := proxy.Serve(proxyListener); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("browser proxy stopped", "error", err)
		}
	}()

	defer func() {
		cancel()
		workers.Wait()
	}()
	workers.Go(func() {
		worker.NewPool(db, checker.NewProcessor(db, httpChecker, chromium, screenshots), app.Worker).Run(ctx)
	})

	service := checker.NewService(db, policy, checker.Limits{
		MaxPendingChecks: app.MaxPendingChecks,
		MaxURLLength:     app.MaxURLLength,
	})
	server := &http.Server{
		Addr: app.Addr(),
		Handler: routes.New(routes.Deps{
			DB:           db,
			Checks:       service,
			MaxBodyBytes: app.MaxBodyBytes,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", server.Addr, "env", app.Env, "db_driver", dbConfig.Driver)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), app.ShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down server: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
