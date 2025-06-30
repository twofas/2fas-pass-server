// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

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

	"github.com/gorilla/mux"
	"github.com/ilyakaznacheev/cleanenv"

	"github.com/twofas/2fas-pass-server/metrics"
	"github.com/twofas/2fas-pass-server/server"
	"github.com/twofas/2fas-pass-server/server/connection"
	"github.com/twofas/2fas-pass-server/server/fcm"
	"github.com/twofas/2fas-pass-server/server/notif"
	"github.com/twofas/2fas-pass-server/server/wstunnel"
)

// Config is a configuration struct for the server.
type Config struct {
	Port                    string        `env:"PORT" env-default:"8080"`
	GracefulShutdownWait    time.Duration `env:"GRACEFUL_SHUTDOWN_WAIT" env-default:"1s"`
	GracefulShutdownTimeout time.Duration `env:"GRACEFUL_SHUTDOWN_TIMEOUT" env-default:"15s"`

	FirebaseCredentials string `env:"FIREBASE_CREDENTIALS"`

	ConnectionPoolConfig connection.Config
	WebSocketConfig      wstunnel.Config

	ClearCacheEvery     time.Duration `env:"CLEAR_CACHE_EVERY" env-default:"5s"`
	CacheTTL            time.Duration `env:"CACHE_TTL" env-default:"5m"`
	MaxPayloadSizeBytes int64         `env:"MAX_PAYLOAD_SIZE_BYTES" env-default:"262144"`

	EnableMetricExporter bool `env:"ENABLE_METRIC_EXPORTER" env-default:"false"`
}

var SourceCommit = "unknown" // This will be set by the build system.

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	logger.Info("Starting 2FAS Pass Server", slog.String("source_commit", SourceCommit))

	if err := start(logger); err != nil {
		logger.Error("Failed to start", slog.Any("error", err))
		os.Exit(1)
	}
}

func start(logger *slog.Logger) error { //nolint:funlen // This is setup code
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var cfg Config
	err := cleanenv.ReadEnv(&cfg)
	if err != nil {
		return fmt.Errorf("failed to read env: %w", err)
	}

	if cfg.EnableMetricExporter {
		shutdown, err := metrics.Setup(context.Background())
		if err != nil {
			return fmt.Errorf("failed to setup metrics exporter: %w", err)
		}
		defer func() {
			err := shutdown(context.Background())
			if err != nil {
				logger.Error("Failed to shutdown metrics exporter", slog.Any("error", err))
			}
		}()
	} else {
		logger.Info("Metrics exporter is disabled")
	}

	r := mux.NewRouter()
	tunnelFactory := func() connection.Tunnel {
		return wstunnel.New(cfg.WebSocketConfig)
	}
	fcmClient, err := initFCMClient(ctx, logger, cfg)
	if err != nil {
		return err
	}
	notifSrv := notif.New()

	connectionPool := connection.NewPool(tunnelFactory, cfg.ConnectionPoolConfig)
	notificationHandler := &server.NotificationHandler{
		Logger:         logger,
		FCMClient:      fcmClient,
		Notifications:  notifSrv,
		TTL:            cfg.CacheTTL,
		MaxMessageSize: cfg.MaxPayloadSizeBytes,
	}

	server.Setup(r, logger, connectionPool, notificationHandler)

	srv := &http.Server{
		Addr: "0.0.0.0:" + cfg.Port,
		// Good practice to set timeouts to avoid Slowloris attacks.
		WriteTimeout: time.Second * 15,
		ReadTimeout:  time.Second * 15,
		IdleTimeout:  time.Second * 60,
		Handler:      r, // Pass our instance of gorilla/mux in.
	}

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down")
		time.Sleep(cfg.GracefulShutdownWait)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.GracefulShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck
			logger.Error("Error at shutdown", slog.Any("error", err))
		}
	}()

	logger.Info("Starting server")
	if err := srv.ListenAndServe(); err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server closed with an error: %w", err)
	}
	return nil
}

func initFCMClient(ctx context.Context, logger *slog.Logger, cfg Config) (fcm.Client, error) {
	if cfg.FirebaseCredentials == "" {
		logger.Info("No Firebase credentials were provided. Will no setup Firebase client")
		return fcm.NewFakePushClient(logger), nil
	}
	logger.Info("Creating Firebase client")
	fcmClient, err := fcm.NewClient(ctx, cfg.FirebaseCredentials, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create fcm client: %w", err)
	}
	return fcmClient, nil
}
