// Copyright (c) 2023-2025 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

package common

import (
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/zipkin"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"go.opentelemetry.io/otel/trace"
)

func NewTracerProvider(serviceName string, environment string, id int64) (*sdkTrace.TracerProvider, error) {
	zipkinEndpoint := GetEnv("OTEL_EXPORTER_ZIPKIN_ENDPOINT", "http://localhost:9411/api/v2/spans")
	exporter, err := zipkin.New(zipkinEndpoint)
	if err != nil {
		return nil, err
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(serviceName),
		attribute.String("environment", environment),
		attribute.Int64("ID", id),
	)

	return sdkTrace.NewTracerProvider(
		sdkTrace.WithBatcher(exporter, sdkTrace.WithBatchTimeout(time.Second*1)),
		sdkTrace.WithResource(res),
		sdkTrace.WithSampler(sdkTrace.AlwaysSample()),
	), nil
}

// NewTracingRoundTripper returns an http.RoundTripper that creates an OTel span
// for every outgoing request and propagates trace context in headers.
func NewTracingRoundTripper() http.RoundTripper {
	return &tracingRoundTripper{
		base:   http.DefaultTransport,
		tracer: otel.Tracer("sdk-transport"),
	}
}

type tracingRoundTripper struct {
	base   http.RoundTripper
	tracer trace.Tracer
}

func (t *tracingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	spanName := fmt.Sprintf("%s %s", req.Method, req.URL.Path)
	ctx, span := t.tracer.Start(req.Context(), spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.HTTPMethodKey.String(req.Method),
			semconv.HTTPURLKey.String(req.URL.String()),
			attribute.String("http.host", req.URL.Host),
		),
	)
	defer span.End()

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}

	span.SetAttributes(semconv.HTTPStatusCodeKey.Int(resp.StatusCode))

	return resp, nil
}
