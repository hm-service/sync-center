package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"sync-center/api"
	"sync-center/app"
	"sync-center/core"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	config, err := core.LoadConfig()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	os.Unsetenv("READYZ_TOKEN")
	os.Unsetenv("AUTH_TOKEN")
	os.Unsetenv("SESSION_SIGNING_KEY")

	application, err := app.New(config)
	if err != nil {
		logger.Error("start application", "error", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 {
		if os.Args[1] != "gc" {
			logger.Error("unknown command", "command", os.Args[1], "usage", "gc")
			os.Exit(1)
		}
		removed, err := application.CollectGarbage(app.GarbageGrace)
		if err != nil {
			logger.Error("collect garbage", "error", err)
			os.Exit(1)
		}
		logger.Info("garbage collected", "removed", removed)
		return
	}

	addr := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	server := &http.Server{
		Addr:              addr,
		Handler:           api.New(application, frontendFS(), logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	failures := make(chan error, 1)
	go func() {
		failures <- server.ListenAndServe()
	}()
	logger.Info("server started",
		"addr", addr,
		"data_dir", config.DataDir,
		"start_at", time.Now().Format(time.RFC3339),
	)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "error", err)
			os.Exit(1)
		}
	case received := <-signals:
		signal.Stop(signals)
		logger.Info("shutdown signal received", "signal", received.String())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("shutdown", "error", err)
			os.Exit(254)
		}
	}

	logger.Info("server shutdown", "stop_at", time.Now().Format(time.RFC3339))
}
