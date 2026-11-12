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

	// gc 只碰数据目录，不加载 namespace 配置与面板密钥
	if len(os.Args) > 1 {
		if os.Args[1] != "gc" {
			logger.Error("unknown command", "command", os.Args[1], "usage", "gc")
			os.Exit(1)
		}
		data, err := app.OpenData(core.DataDirFromEnv())
		if err != nil {
			logger.Error("open data dir", "error", err)
			os.Exit(1)
		}
		removed, err := data.CollectGarbage(app.GarbageGrace)
		if err != nil {
			logger.Error("collect garbage", "error", err)
			os.Exit(1)
		}
		logger.Info("garbage collected", "removed", removed)
		return
	}

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

	if config.GCInterval > 0 {
		go runGarbageCollector(application.Data, config.GCInterval, logger)
	}

	addr := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	server := &http.Server{
		Addr:              addr,
		Handler:           api.New(application, frontendFS(), logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
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

// runGarbageCollector 启动时先清理一次，之后按间隔重复。
// 清理持写锁，期间的请求排队等待，通常只有毫秒级。
func runGarbageCollector(data *app.Data, interval time.Duration, logger *slog.Logger) {
	collect := func() {
		started := time.Now()
		removed, err := data.CollectGarbage(app.GarbageGrace)
		if err != nil {
			logger.Error("collect garbage", "error", err)
			return
		}
		logger.Info("garbage collected",
			"removed", removed,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
	collect()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		collect()
	}
}
