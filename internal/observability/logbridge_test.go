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

package observability_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/donaldgifford/renovate-operator/internal/observability"
)

// captureLogger returns a logr.Logger that writes formatted log lines to buf.
// The prefix arg is unused — funcr's signature passes prefix and formatted
// args; only the args are interesting for assertion.
func captureLogger(buf *bytes.Buffer) logr.Logger {
	return funcr.New(
		func(_, args string) { buf.WriteString(args + "\n") },
		funcr.Options{},
	)
}

func TestLogrFromContext_NoSpanReturnsBaseLogger(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	base := captureLogger(buf)
	ctx := logr.NewContext(context.Background(), base)

	out := observability.LogrFromContext(ctx)
	out.Info("hello")

	logged := buf.String()
	if !strings.Contains(logged, "hello") {
		t.Errorf("log output = %q, want to contain 'hello'", logged)
	}
	if strings.Contains(logged, "trace_id") {
		t.Errorf("trace_id should not be attached when no span is recording, got: %s", logged)
	}
}

func TestLogrFromContext_RecordingSpanAttachesTraceFields(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "op")
	defer span.End()

	if !span.IsRecording() {
		t.Fatal("test span should be recording")
	}

	buf := &bytes.Buffer{}
	base := captureLogger(buf)
	ctx = logr.NewContext(ctx, base)

	out := observability.LogrFromContext(ctx)
	out.Info("inside-span")

	logged := buf.String()
	if !strings.Contains(logged, "trace_id") {
		t.Errorf("expected trace_id in log line, got: %s", logged)
	}
	if !strings.Contains(logged, "span_id") {
		t.Errorf("expected span_id in log line, got: %s", logged)
	}

	sc := span.SpanContext()
	if !strings.Contains(logged, sc.TraceID().String()) {
		t.Errorf("expected trace ID %s in log, got: %s", sc.TraceID(), logged)
	}
}

func TestLogrFromContext_NoBaseLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()
	// FromContextOrDiscard returns a discard logger; calling Info on it is
	// a no-op. The test asserts we don't panic and don't blow up trying
	// to attach trace fields when there's no span.
	out := observability.LogrFromContext(context.Background())
	out.Info("ignored")
}

func TestTracer_ReturnsNonNilDefaultProvider(t *testing.T) {
	t.Parallel()
	// Even with no exporter wired, otel.Tracer returns the noop tracer —
	// callers can safely .Start spans without a nil-check.
	tr := observability.Tracer()
	if tr == nil {
		t.Fatal("Tracer() = nil")
	}
	_, span := tr.Start(context.Background(), "noop-test")
	span.End()
	if span == nil {
		t.Error("Start span returned nil")
	}
	if !trace.SpanFromContext(context.Background()).SpanContext().IsValid() {
		// Default span has invalid context — that's fine, we just verified
		// .Start returned without error.
		_ = span
	}
}
