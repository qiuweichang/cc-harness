// Package main starts the cc-hardness local Web control plane.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cc-hardness/internal/bootstrap"
	"cc-hardness/internal/catalog"
	"cc-hardness/internal/config"
	"cc-hardness/internal/configset"
	"cc-hardness/internal/httpapi"
	ccruntime "cc-hardness/internal/runtime"
	"cc-hardness/internal/task"
)

// main assembles the file-backed task service and starts the HTTP server.
// The process listens for operating-system shutdown signals so active requests
// can finish before the local control plane exits.
func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	catalogRepository, err := catalog.NewRepository(cfg.DataRoot)
	if err != nil {
		logger.Error("failed to initialize catalog repository", "error", err, "data_root", cfg.DataRoot)
		os.Exit(1)
	}
	catalogService := catalog.NewService(catalogRepository)
	if err := bootstrap.InstallDefaultCatalog(catalogService); err != nil {
		logger.Error("failed to install default catalog", "error", err)
		os.Exit(1)
	}

	configSetRepository, err := configset.NewRepository(cfg.DataRoot)
	if err != nil {
		logger.Error("failed to initialize configuration set repository", "error", err, "data_root", cfg.DataRoot)
		os.Exit(1)
	}
	configSetService := configset.NewService(configSetRepository)

	repository, err := task.NewRepository(cfg.DataRoot)
	if err != nil {
		logger.Error("failed to initialize task repository", "error", err, "data_root", cfg.DataRoot)
		os.Exit(1)
	}

	taskService := task.NewService(repository, catalogService, configSetService)
	runtimeManager := ccruntime.NewManager(logger)
	handler := httpapi.NewHandler(catalogService, configSetService, taskService, runtimeManager, cfg.WebRoot, logger)
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("cc-hardness web server started", "address", cfg.Address, "data_root", cfg.DataRoot)
		if serveErr := server.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("web server stopped unexpectedly", "error", serveErr)
			stop()
		}
	}()

	<-shutdownContext.Done()
	gracefulContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtimeManager.StopAll(); err != nil {
		logger.Error("failed to stop all Claude sessions", "error", err)
	}
	if err := server.Shutdown(gracefulContext); err != nil {
		logger.Error("web server shutdown failed", "error", err)
		return
	}
	logger.Info("cc-hardness web server stopped")
}
