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
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"
)

// StartPprof starts an HTTP server exposing /debug/pprof/* on bindAddress.
// Returns a shutdown func the caller defers. When bindAddress is empty
// pprof is disabled and the shutdown is a no-op.
func StartPprof(bindAddress string) (func(context.Context) error, error) {
	if bindAddress == "" {
		return func(context.Context) error { return nil }, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              bindAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("pprof: ListenAndServe %s: %w", bindAddress, err)
			return
		}
		errCh <- nil
	}()

	return func(ctx context.Context) error {
		if err := srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("pprof: shutdown: %w", err)
		}
		return <-errCh
	}, nil
}
