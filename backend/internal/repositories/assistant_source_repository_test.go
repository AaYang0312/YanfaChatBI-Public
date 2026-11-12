package repositories

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"yanfachatbi/internal/models"
)

// assistantStubSource is the minimal harness for the snapshot gate. Counters
// prove whether fact methods were reached behind the gate; they never stand in
// for the PostgreSQL acceptance, which runs only with a real DSN.
type assistantStubSource struct {
	capabilities models.SourceCapabilities
	capErr       error
	snapshot     models.FactSnapshot
	salesErr     error
	salesCalls   int
}

func (s *assistantStubSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	return s.capabilities, s.capErr
}

func (s *assistantStubSource) ResolveEntities(ctx context.Context, query AssistantEntityQuery) ([]models.Candidate, error) {
	return nil, nil
}

func (s *assistantStubSource) ReadCoverage(ctx context.Context, query AssistantCoverageQuery) ([]AssistantCoverage, error) {
	return nil, nil
}

func (s *assistantStubSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.salesCalls++
	return s.snapshot, s.salesErr
}

func (s *assistantStubSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	return models.FactSnapshot{}, nil
}

func TestAssistantReadSnapshotGate(t *testing.T) {
	query := models.FactQuery{
		Basis: models.BasisPayment,
		Window: models.DateRange{
			StartDate:        "2026-09-01",
			EndDateExclusive: "2026-09-08",
		},
	}

	t.Run("an empty contract version refuses the read with capability_unavailable", func(t *testing.T) {
		source := &assistantStubSource{capabilities: models.SourceCapabilities{}}
		snapshot, err := AssistantReadSnapshot(context.Background(), source, query)
		if err == nil {
			t.Fatal("AssistantReadSnapshot succeeded without an approved contract version")
		}
		validation, ok := models.AsValidationError(err)
		if !ok {
			t.Fatalf("error is not a models.ValidationError: %v", err)
		}
		if validation.Code != models.ErrorCodeCapabilityUnavailable {
			t.Fatalf("code = %s want %s", validation.Code, models.ErrorCodeCapabilityUnavailable)
		}
		if validation.Field != "source.contract_version" {
			t.Fatalf("field = %q want %q", validation.Field, "source.contract_version")
		}
		if snapshot.Rows != nil || snapshot.Basis != "" || snapshot.ContractVersion != "" || snapshot.Window != (models.DateRange{}) {
			t.Fatalf("snapshot must be zero on refusal, got %+v", snapshot)
		}
		if source.salesCalls != 0 {
			t.Fatalf("ReadSalesFacts called %d times behind the gate; must be 0", source.salesCalls)
		}
	})

	t.Run("a declared contract version reads the sales snapshot", func(t *testing.T) {
		want := models.FactSnapshot{ContractVersion: "test-v1"}
		source := &assistantStubSource{
			capabilities: models.SourceCapabilities{ContractVersion: "test-v1"},
			snapshot:     want,
		}
		got, err := AssistantReadSnapshot(context.Background(), source, query)
		if err != nil {
			t.Fatalf("AssistantReadSnapshot: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot = %+v want %+v", got, want)
		}
		if source.salesCalls != 1 {
			t.Fatalf("ReadSalesFacts calls = %d want 1", source.salesCalls)
		}
	})

	t.Run("a capabilities error surfaces unchanged", func(t *testing.T) {
		sentinel := errors.New("capabilities probe failed")
		source := &assistantStubSource{capErr: sentinel}
		if _, err := AssistantReadSnapshot(context.Background(), source, query); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v want the capabilities error", err)
		}
		if source.salesCalls != 0 {
			t.Fatalf("ReadSalesFacts calls = %d want 0", source.salesCalls)
		}
	})
}

// compile-time interface check: the stub must satisfy the boundary.
var _ AssistantSourceRepository = (*assistantStubSource)(nil)
