package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/elven-observability/go-otel-app/internal/platform/bootstrap"
	"github.com/elven-observability/go-otel-app/internal/platform/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	serverErrCh := make(chan error, 1)
	go func() {
		app.Logger.Info("starting HTTP server", "addr", cfg.HTTPAddr)
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	go func() {
		if err := app.Worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			app.Logger.Error("worker exited", "error", err.Error())
			stop()
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrCh:
		if err != nil {
			app.Logger.Error("server exited", "error", err.Error())
		}
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := app.Server.Shutdown(shutdownCtx); err != nil {
		app.Logger.Error("server shutdown failed", "error", err.Error())
	}
}
