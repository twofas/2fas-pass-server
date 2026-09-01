// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package metrics

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func Setup(ctx context.Context, enableExporter bool) (func(context.Context) error, error) {
	shutdownFuncs := make([]func(context.Context) error, 0, 2)
	var err error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	runtimeReader := metric.NewManualReader(
		// Add the runtime producer to get histograms from the Go runtime.
		metric.WithProducer(runtime.NewProducer()),
	)
	err = runtime.Start()
	if err != nil {
		handleErr(err)
		return shutdown, fmt.Errorf("failed to start runtime metrics: %w", err)
	}

	// Set up meter provider.
	meterProvider, newMeterProviderErr := newMeterProvider(ctx, enableExporter, runtimeReader)
	if newMeterProviderErr != nil {
		handleErr(newMeterProviderErr)
		return shutdown, fmt.Errorf("failed to start runtime metrics: %w", err)
	}

	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	tracerProvider := newTraceProvider()
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	return shutdown, nil
}

func newMeterProvider(ctx context.Context, enableExporter bool, runtime metric.Reader) (*metric.MeterProvider, error) {
	options := []metric.Option{metric.WithReader(runtime)}

	if enableExporter {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to obtain OTLP HTTP exporter: %w", err)
		}
		options = append(options, metric.WithReader(metric.NewPeriodicReader(exp)))
	}

	meterProvider := metric.NewMeterProvider(options...)
	return meterProvider, nil
}

func newTraceProvider() *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider()
}
