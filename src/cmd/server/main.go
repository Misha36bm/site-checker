// Command server runs the site-checker API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/api/routes"
	"github.com/Misha36bm/site-checker/src/internal/config"
	"github.com/Misha36bm/site-checker/src/internal/database"
	_ "github.com/Misha36bm/site-checker/src/internal/database/postgres" // registers the driver
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

	server := &http.Server{
		Addr:              app.Addr(),
		Handler:           routes.New(db),
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), app.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down server: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
