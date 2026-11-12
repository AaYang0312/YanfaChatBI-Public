// Package embeddingmaintenance prepares and simulates an empty-index bootstrap.
// It has no live adapters: supplied JSON is untrusted metadata, not attestation.
package embeddingmaintenance

import "time"

type Pair struct {
	Schema   string `json:"schema"`
	Messages string `json:"messages"`
}
type Source struct {
	EligibleTexts *uint64 `json:"eligible_texts"`
	Provenance    string  `json:"provenance"`
}
type Spec struct {
	Provider               string `json:"provider"`
	Model                  string `json:"model"`
	Endpoint               string `json:"endpoint"`
	Dimensions             int    `json:"dimensions"`
	Distance               string `json:"distance"`
	Old                    Pair   `json:"old"`
	Target                 Pair   `json:"target"`
	MaxInventoryAgeSeconds int64  `json:"max_inventory_age_seconds"`
	Source                 Source `json:"source"`
}

// Collection describes a physical collection, not an alias or named-vector map.
// A nil PointsCount means unknown; zero and exact-count evidence must be supplied explicitly.
// PointsCountExact describes the supplied audit method, not an attestation by this tool.
type Collection struct {
	Name             string  `json:"name"`
	VectorType       string  `json:"vector_type"`
	Dimensions       int     `json:"dimensions"`
	Distance         string  `json:"distance"`
	PointsCount      *uint64 `json:"points_count"`
	PointsCountExact bool    `json:"points_count_exact"`
	Status           string  `json:"status"`
}
type Alias struct {
	Name       string `json:"name"`
	Collection string `json:"collection"`
}
type Inventory struct {
	InstanceID          string       `json:"instance_id"`
	ObservedAt          time.Time    `json:"observed_at"`
	Provenance          string       `json:"provenance"`
	CollectionsComplete bool         `json:"collections_complete"`
	AliasesComplete     bool         `json:"aliases_complete"`
	Collections         []Collection `json:"collections"`
	Aliases             []Alias      `json:"aliases"`
	OldProvider         string       `json:"old_provider"`
	OldModel            string       `json:"old_model"`
}
type Estimate struct {
	BatchSize     int     `json:"batch_size"`
	Requests      *uint64 `json:"requests"`
	DurationKnown bool    `json:"duration_known"`
	Explanation   string  `json:"explanation"`
}
type Plan struct {
	Version                  int       `json:"version"`
	ID                       string    `json:"id"`
	Kind                     string    `json:"kind"`
	Spec                     Spec      `json:"spec"`
	Baseline                 Inventory `json:"baseline"`
	LiveReady                bool      `json:"live_ready"`
	PauseAttested            bool      `json:"pause_attested"`
	OperationalRollbackReady bool      `json:"operational_rollback_ready"`
	HistoricalCoverage       string    `json:"historical_coverage"`
	PendingPrerequisites     []string  `json:"pending_prerequisites"`
	Estimate                 Estimate  `json:"estimate"`
}
type Preflight struct {
	MetadataConsistent       bool     `json:"metadata_consistent"`
	LiveReady                bool     `json:"live_ready"`
	PauseAttested            bool     `json:"pause_attested"`
	OperationalRollbackReady bool     `json:"operational_rollback_ready"`
	HistoricalCoverage       string   `json:"historical_coverage"`
	PendingPrerequisites     []string `json:"pending_prerequisites"`
}
type Scenario struct {
	FailAt       string `json:"fail_at"`
	Rollback     bool   `json:"rollback"`
	ResumeWrites bool   `json:"resume_writes"`
}
type Event struct {
	Action   string `json:"action"`
	Selected Pair   `json:"selected"`
}
type Simulation struct {
	OperationalRollbackReady bool         `json:"operational_rollback_ready"`
	SimulationOnly           bool         `json:"simulation_only"`
	LiveReady                bool         `json:"live_ready"`
	PauseAttested            bool         `json:"pause_attested"`
	HistoricalCoverage       string       `json:"historical_coverage"`
	Phase                    string       `json:"phase"`
	Selected                 Pair         `json:"selected"`
	Collections              []Collection `json:"collections"`
	Events                   []Event      `json:"events"`
	Failure                  string       `json:"failure,omitempty"`
	InstantRollbackAllowed   bool         `json:"instant_rollback_allowed"`
	NewMaintenanceRequired   bool         `json:"new_maintenance_required"`
}
