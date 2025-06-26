// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.opentelemetry.io/otel/attribute"
	semconvNew "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type responseRecorder struct {
	http.ResponseWriter
	http.Hijacker
	statusCode       int
	bytesTransferred int64
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.statusCode == 0 {
		r.statusCode = statusCode
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	if err != nil {
		return n, err //nolint:wrapcheck // Not needed here.
	}
	r.bytesTransferred += int64(n)
	return n, nil
}

func loggingMiddleware(logger *slog.Logger) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := mux.CurrentRoute(r)
			var routeName string
			if route != nil {
				routeName = route.GetName()
			} else {
				routeName = "unknown"
			}

			start := time.Now()
			logger.Info(
				"request started",
				slog.String("route_name", routeName),
			)
			h, ok := w.(http.Hijacker)
			if !ok {
				logger.Error("response writer does not support hijacking",
					slog.String("route_name", routeName),
					slog.String("error", "response writer does not implement http.Hijacker"),
				)
				next.ServeHTTP(w, r)
				return
			}

			rr := &responseRecorder{
				ResponseWriter: w,
				Hijacker:       h,
			}

			next.ServeHTTP(rr, r)
			duration := time.Since(start)
			logger.Info("request finished",
				slog.String("route_name", routeName),
				slog.Int64("duration_ms", duration.Milliseconds()),
				slog.Int("status_code", rr.statusCode),
				slog.Int64("bytes_transferred", rr.bytesTransferred),
			)
		})
	}
}

func metricsMiddleware() mux.MiddlewareFunc {
	return otelmux.Middleware("2pass", otelmux.WithMetricAttributesFn(func(r *http.Request) []attribute.KeyValue {
		route := mux.CurrentRoute(r)
		if route == nil {
			return []attribute.KeyValue{
				semconvNew.HTTPRouteKey.String("unknown"),
			}
		}
		return []attribute.KeyValue{
			semconvNew.HTTPRouteKey.String(route.GetName()),
		}
	}))
}
