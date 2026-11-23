package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
)

// TestMetricsAndHelpers builds the instruments against a no-op provider, which is what the gateway
// gives them: every instrument must exist and every recording must be harmless. The provider is
// set here because TestNewMetricsInstrumentErrors leaves a failing one behind (the global cannot be
// handed back to itself).
func TestMetricsAndHelpers(t *testing.T) {
	otel.SetMeterProvider(noop.NewMeterProvider())
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	Count(ctx, m.CredentialAccessed, 1, Outcome(true))
	Count(ctx, m.AuditDropped, 1, Outcome(false))
	Count(ctx, nil, 1) // nil-safe no-op
	if Outcome(true).Value.AsString() != "success" || Outcome(false).Value.AsString() != "failure" {
		t.Fatal("outcome attr")
	}
}
