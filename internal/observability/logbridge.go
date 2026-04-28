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

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/trace"
)

// LogrFromContext wraps logr.FromContext, adding trace_id / span_id keys
// when the active span is recording. Reconcilers should call this instead
// of logr.FromContext directly so log lines correlate to traces.
func LogrFromContext(ctx context.Context) logr.Logger {
	log := logr.FromContextOrDiscard(ctx)
	if !trace.SpanFromContext(ctx).IsRecording() {
		return log
	}
	sc := trace.SpanFromContext(ctx).SpanContext()
	return log.WithValues(
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	)
}
