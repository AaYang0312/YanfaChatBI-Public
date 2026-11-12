package repositories

import (
	"context"

	"yanfachatbi/internal/models"
)

// Assistant v1 source boundary (T05).
//
// AssistantSourceRepository is the only way the assistant metric engine may
// touch a data source. Every parameter is a typed identifier, date range,
// basis, or scope taken from models.FactQuery — never a string of SQL, a
// table name, or a column name. Implementations translate these typed inputs
// into approved fixed statements with bound parameters, executed on the
// dedicated read-only connection (pkg/dbmanager.AssistantReadonlyReader,
// sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}).
//
// Q01–Q05 are open, so no production SQL exists yet: this file ships the
// boundary, not queries. Real mappings arrive with T19 and must stay fixed
// statements; missing mappings return capability_unavailable.

// AssistantEntityQuery is the typed input of ResolveEntities: which product or
// SKU identities to resolve, inside which approved scope.
type AssistantEntityQuery struct {
	Product models.ProductSelection
	Scope   models.Scope
}

// AssistantCoverageQuery is the typed input of ReadCoverage. Product carries
// the same confirmed product/SKU selection as the fact read, so a source can
// answer coverage for exactly the product the engine is reporting on; the zero
// value means the comparison workflow has no product restriction.
type AssistantCoverageQuery struct {
	Basis   models.Basis
	Window  models.DateRange
	Scope   models.Scope
	Product models.ProductSelection
}

// AssistantCoverage is one entity's declared source coverage for a basis and
// window. Coverage is read from the source, never inferred from sales facts,
// and refund coverage is independent of sales coverage.
type AssistantCoverage struct {
	EntityType models.EntityType
	EntityID   string
	Status     models.CoverageStatus
}

// AssistantSourceRepository is the enum-only boundary between assistant v1 and
// a customer data source. No method accepts or returns SQL text.
type AssistantSourceRepository interface {
	// Capabilities declares the approved source contract. An empty
	// ContractVersion means no approved contract: AssistantReadSnapshot
	// refuses to read facts from such a source.
	Capabilities(ctx context.Context) (models.SourceCapabilities, error)
	// ResolveEntities resolves typed product/SKU selections to entity
	// candidates inside the approved scope. It never returns metric values.
	ResolveEntities(ctx context.Context, query AssistantEntityQuery) ([]models.Candidate, error)
	// ReadCoverage reads per-entity coverage for one basis and window.
	ReadCoverage(ctx context.Context, query AssistantCoverageQuery) ([]AssistantCoverage, error)
	// ReadSalesFacts reads verified sales facts for the typed query as one
	// consistent read-only snapshot.
	ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error)
	// ReadRefundFacts reads refund facts for the typed query. Refund rows are
	// separate from sales rows; refund semantics stay governed by Q02/Q04 and
	// are never inferred here.
	ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error)
}

// AssistantReadSnapshot gates one snapshot read on the declared source
// contract. A source whose ContractVersion is empty has no approved data
// contract: the read returns capability_unavailable and no fact method is
// called. Whether the requested basis and metrics are supported is validated
// by the caller against the same capabilities; unknown capabilities never
// become enabled.
func AssistantReadSnapshot(ctx context.Context, source AssistantSourceRepository, query models.FactQuery) (models.FactSnapshot, error) {
	capabilities, err := source.Capabilities(ctx)
	if err != nil {
		return models.FactSnapshot{}, err
	}
	if capabilities.ContractVersion == "" {
		return models.FactSnapshot{}, models.NewValidationError(
			models.ErrorCodeCapabilityUnavailable,
			"source.contract_version",
			"the data source has no approved contract version; assistant fact reads are unavailable",
		)
	}
	return source.ReadSalesFacts(ctx, query)
}
