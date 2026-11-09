package telemetry

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// End finishes span and, when err is a real failure, records it on the span.
//
// isExpected reports errors that are part of normal operation (validation
// failures, not found, conflicts). They are client errors, not service
// failures, so they must not show up as red spans or inflate error rates.
func End(span trace.Span, err error, isExpected func(error) bool) {
	if err != nil {
		if isExpected != nil && isExpected(err) {
			span.SetAttributes(attribute.String("error.kind", "expected"), attribute.String("error.message", err.Error()))
		} else {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}
	span.End()
}
