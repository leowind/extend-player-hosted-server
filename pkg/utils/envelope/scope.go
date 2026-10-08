// Copyright (c) 2021-2023 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

package envelope

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/AccelByte/go-restful-plugins/v3/pkg/trace"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"extend-player-hosted-server/pkg/constants"
)

const (
	abTraceIdLogField       = "abTraceID"
	serviceName             = "justice-session-service"
	gitHashField            = "gitHash"
	versionField            = "serviceVersion"
	roleSeedingVersionField = "roleSeedingVersion"
)

// Scope used as the envelope to combine and transport request-related information by the chain of function calls.
type Scope struct {
	Ctx     context.Context //nolint:containedctx
	TraceID string
	span    oteltrace.Span
	Log     *slog.Logger
}

func ChildScopeFromRemoteScope(ctx context.Context, name string, traceID string) *Scope {
	tracer := otel.Tracer(serviceName)
	tracerCtx, span := tracer.Start(ctx, name)

	return &Scope{
		Ctx:     tracerCtx,
		TraceID: traceID,
		span:    span,
		Log: slog.With(
			abTraceIdLogField, traceID,
			versionField, constants.VERSION,
			gitHashField, constants.GIT_HASH,
			roleSeedingVersionField, constants.ROLE_SEEDING_VERSION,
		),
	}
}

func NewRootScope(rootCtx context.Context, name string, abTraceID string) *Scope {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(rootCtx, name)
	scope := &Scope{
		Ctx:     ctx,
		TraceID: abTraceID,
		span:    span,
		Log: slog.With(
			abTraceIdLogField, abTraceID,
			versionField, constants.VERSION,
			gitHashField, constants.GIT_HASH,
			roleSeedingVersionField, constants.ROLE_SEEDING_VERSION,
		),
	}

	if abTraceID != "" {
		scope.TraceTag(trace.TraceIDKey, abTraceID)
	}

	return scope
}

// Finish finishes current scope.
// Finish finishes current scope.
func (s *Scope) Finish() {
	s.span.End()
}

// TraceError records an error and sets the span status with that error so it can be viewed.
func (s *Scope) TraceError(err error) {
	s.span.RecordError(err)
	s.span.SetStatus(codes.Error, err.Error())
}

// TraceTag sends a tag into tracer.
func (s *Scope) TraceTag(key, value string) {
	s.AddBaggage(key, value)
}

// AddBaggage adds metadata to the span. Use SetAttributes for other value objects besides a String.
func (s *Scope) AddBaggage(key string, value string) {
	s.span.SetAttributes(attribute.String(key, value))
}

// GetSpanContextString gets scope span context string.
func (s *Scope) GetSpanContextString() string {
	return s.span.SpanContext().SpanID().String()
}

// NewChildScope creates new child Scope.
func (s *Scope) NewChildScope(name string) *Scope {
	tracer := s.span.TracerProvider().Tracer(serviceName)
	ctx, span := tracer.Start(s.Ctx, name)

	return &Scope{
		Ctx:     ctx,
		TraceID: s.TraceID,
		span:    span,
		Log:     s.Log,
	}
}

// NewChildScope creates new child Scope.
func (s *Scope) NewChildScopeWithTimeout(name string, timeout time.Duration) *Scope {
	tracer := s.span.TracerProvider().Tracer(serviceName)
	ctx, _ := context.WithTimeout(s.Ctx, timeout) //nolint:govet
	ctx, span := tracer.Start(ctx, name)

	return &Scope{
		Ctx:     ctx,
		TraceID: s.TraceID,
		span:    span,
		Log:     s.Log,
	}
}

func (s Scope) SetName(name string) {
	s.span.SetName(name)
}

// SetAttributes adds attributes onto a span based on the value object type
// TODO https://accelbyte.atlassian.net/browse/AR-3931
//
//nolint:cyclop
func (s *Scope) SetAttributes(key string, value interface{}) {
	switch v := value.(type) {
	case bool:
		s.span.SetAttributes(attribute.Bool(key, v))
	case string:
		s.span.SetAttributes(attribute.String(key, v))
	case int:
		s.span.SetAttributes(attribute.Int(key, v))
	case int64:
		s.span.SetAttributes(attribute.Int64(key, v))
	case float64:
		s.span.SetAttributes(attribute.Float64(key, v))
	case []bool:
		s.span.SetAttributes(attribute.BoolSlice(key, v))
	case []string:
		s.span.SetAttributes(attribute.StringSlice(key, v))
	case []int:
		s.span.SetAttributes(attribute.IntSlice(key, v))
	case []int64:
		s.span.SetAttributes(attribute.Int64Slice(key, v))
	case []float64:
		s.span.SetAttributes(attribute.Float64Slice(key, v))
	default:
		slog.Error("could not set a span attribute", "type", fmt.Sprintf("%T", value))
	}
}

// TraceEvent creates a human-readable message on the span -- typically representing that "something happened".
func (s *Scope) TraceEvent(eventMessage string) {
	s.span.AddEvent(eventMessage)
}

// SetLogger allows for setting a different logger than the default std logger. This is mostly useful for testing.
func (s *Scope) SetLogger(logger *slog.Logger) {
	s.Log = logger.With(abTraceIdLogField, s.TraceID)
}
