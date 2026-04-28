/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package observability

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is the OTel service.name attribute the operator advertises.
const ServiceName = "renovate-operator"

// noopShutdown is returned when tracing is disabled. Calling it is a no-op
// so cmd/main.go can defer it unconditionally.
var noopShutdown = func(context.Context) error { return nil }

// InitTracer wires up an OTLP gRPC TracerProvider when the
// OTEL_EXPORTER_OTLP_ENDPOINT env var is set. If unset, returns a
// no-op shutdown so callers can defer it unconditionally without
// branching.
//
// version flows into the resource's service.version attribute.
func InitTracer(ctx context.Context, version string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return noopShutdown, nil
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return noopShutdown, fmt.Errorf("otel: build OTLP gRPC exporter: %w", err)
	}

	// resource.Default()'s schema URL drifts with the SDK; merging a
	// hard-pinned semconv schema produces a "conflicting Schema URL"
	// error. resource.NewSchemaless skips the merge and lets the SDK
	// stamp its own schema on the merged resource.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("otel: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// Tracer returns the operator's named tracer. Reconcilers and builders use
// this to start spans on hot paths.
func Tracer() trace.Tracer {
	return otel.Tracer(ServiceName)
}
