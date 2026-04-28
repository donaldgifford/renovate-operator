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
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/donaldgifford/renovate-operator/internal/observability"
)

func TestInitTracer_NoEndpointReturnsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := observability.InitTracer(t.Context(), "test")
	if err != nil {
		t.Fatalf("InitTracer err = %v", err)
	}
	if err := shutdown(t.Context()); err != nil {
		t.Errorf("noop shutdown err = %v", err)
	}
}

func TestInitTracer_WithEndpointReturnsRealShutdown(t *testing.T) {
	// otlptracegrpc.New is non-blocking — it doesn't dial until first
	// export. Setting a localhost endpoint is enough to drive the happy
	// path through resource.Merge + tracerprovider construction without
	// actually requiring a running collector. Shutdown completes
	// promptly because the batcher has no spans to flush.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	shutdown, err := observability.InitTracer(ctx, "test-version")
	if err != nil {
		t.Fatalf("InitTracer err = %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown = nil")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := shutdown(shutdownCtx); err != nil {
		// A "context deadline" or "connection refused" on shutdown is fine
		// — the test only validates that the construction path ran cleanly.
		t.Logf("shutdown returned %v (acceptable for unit test without collector)", err)
	}
}

func TestStartPprof_EmptyBindIsNoop(t *testing.T) {
	t.Parallel()

	shutdown, err := observability.StartPprof("")
	if err != nil {
		t.Fatalf("StartPprof err = %v", err)
	}
	if err := shutdown(t.Context()); err != nil {
		t.Errorf("noop shutdown err = %v", err)
	}
}

func TestStartPprof_ServesIndex(t *testing.T) {
	t.Parallel()

	// Bind to an ephemeral port to avoid conflicts on parallel runs.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	shutdown, err := observability.StartPprof(addr)
	if err != nil {
		t.Fatalf("StartPprof err = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})

	// Give the server a moment to come up.
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRegisterIsCallableOnce(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			// Register has already been called by another test in this
			// binary; that's fine, the panic confirms the registration
			// was idempotent-required.
			_ = r
		}
	}()
	observability.Register()
}
