// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/twofas/2fas-pass-server/server/connection"
	"github.com/twofas/2fas-pass-server/server/ws"
)

const name = "github.com/twofas/2fas-pass-server"

var (
	meter   = otel.Meter(name)
	rollCnt metric.Int64Counter
)

func init() { //nolint: gochecknoinits // This is how you setup metrics.
	var err error
	rollCnt, err = meter.Int64Counter("health.calls")
	if err != nil {
		panic(err)
	}
}

func health(writer http.ResponseWriter, req *http.Request) {
	rollCnt.Add(req.Context(), 1)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("OK"))
}

// Setup http (and ws) server.
func Setup(
	r *mux.Router,
	logger *slog.Logger,
	pool *connection.Pool,
	notif *NotificationHandler,
) {
	r.HandleFunc("/health", health).
		Methods("GET").
		Name("GET health")

	r.HandleFunc("/proxy/browser_extension/{connection_id}", serveBrowserExtension(logger, pool)).
		Name("WS proxy/browser_extension/{connection_id}")
	r.HandleFunc("/proxy/mobile/{connection_id}", serveMobile(logger, pool)).
		Name("WS proxy/mobile/{connection_id}")
	r.HandleFunc("/notifications", notif.Add).Methods("POST").
		Name("POST notifications")
	r.HandleFunc("/time", serveTime).
		Name("GET time")
	r.HandleFunc("/device/{device_id}/notifications", notif.Get).
		Name("GET device/{device_id}/notifications")
	r.HandleFunc("/device/{device_id}/notifications/{notification_id}", notif.Delete).
		Methods("DELETE").Name("DELETE device/{device_id}/notifications/{notification_id}")

	r.Use(
		metricsMiddleware(),
		loggingMiddleware(logger),
	)
}

func serveMobile(logger *slog.Logger, pool *connection.Pool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		serve(logger, pool, w, req, proxyDirectionMobile)
	}
}

func serveBrowserExtension(logger *slog.Logger, pool *connection.Pool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		serve(logger, pool, w, req, proxyDirectionBrowserExtension)
	}
}

type proxyDirection string

const (
	proxyDirectionMobile           proxyDirection = "mobile"
	proxyDirectionBrowserExtension proxyDirection = "browser_extension"
)

func serve(logger *slog.Logger,
	pool *connection.Pool,
	w http.ResponseWriter,
	req *http.Request,
	direction proxyDirection,
) {
	conn, err := upgrade(w, req)
	if err != nil {
		logger.Error("failed to upgrade proxy", slog.Any("error", err))
		writeError(logger, w, http.StatusBadRequest, fmt.Sprintf("failed to upgrade proxy: %v", err))
		return
	}
	defer func() {
		_ = conn.Close()
	}()

	vars := mux.Vars(req)
	connectionID := vars["connection_id"]
	if connectionID == "" {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation,
			"connection_id is required"))
		return
	}

	monitoredConn := ws.NewMonitoredConn(conn, connectionID, string(direction), logger)

	handler, err := getProxyHandler(pool, direction, connectionID)
	if err != nil {
		switch {
		case errors.Is(err, connection.ErrLeftNotConnected):
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(connection.CloseBrowserExtensionNotConnected,
					"Browser Extension not connected"))
		case errors.Is(err, connection.ErrAlreadyConnected):
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(connection.CloseConnectionAlreadyEstablished,
					fmt.Sprintf("Connection for %q already established", direction)))
		default:
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr,
				"failed to get proxy handler"))
			logger.Error("failed to get proxy handler", slog.Any("error", err))
		}
		return
	}

	if err := handler(monitoredConn); err != nil {
		logger.Error("failed to serve proxy", slog.Any("error", err))
	}
}

func getProxyHandler(pool *connection.Pool, direction proxyDirection, id string) (connection.WebsocketHandler, error) {
	switch direction {
	case proxyDirectionMobile:
		h, err := pool.ServeRight(id)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare proxy handler for mobile: %w", err)
		}
		return h, nil
	case proxyDirectionBrowserExtension:
		h, err := pool.ServeLeft(id)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare proxy handler for browser extension: %w", err)
		}
		return h, nil
	default:
		panic(fmt.Sprintf("unknown direction: %q", direction))
	}
}

func serveTime(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(writer, "%d", time.Now().UnixMilli())
}

type PushToMobileRequest struct {
	FCMToken      string            `json:"fcmToken"`
	FCMTargetType string            `json:"fcmTargetType"`
	DeviceID      string            `json:"deviceId"`
	Data          map[string]string `json:"data"`
}

func writeError(logger *slog.Logger, w http.ResponseWriter, status int, msg string) {
	payload := struct {
		Error string `json:"error"`
	}{
		Error: msg,
	}
	bb, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal response error payload", slog.Any("error", err))
		writeJSONContent(w, http.StatusInternalServerError, `{"error": "Internal server error"}`)
		return
	}
	writeJSONContent(w, status, bb)
}

func writeJSONContent[T interface{ ~string | ~[]byte }](w http.ResponseWriter, status int, bb T) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(bb))
}
