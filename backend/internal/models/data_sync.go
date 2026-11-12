package models

import (
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const DataSyncRunnerVersion = "yanfa-source-sync/1"

const (
	DataSyncOperation            = "sync"
	ExternalMaintenanceOperation = "external_maintenance"
)

type DataSyncStatus string

const (
	DataSyncCreated     DataSyncStatus = "created"
	DataSyncRunning     DataSyncStatus = "running"
	DataSyncSucceeded   DataSyncStatus = "succeeded"
	DataSyncFailed      DataSyncStatus = "failed"
	DataSyncInterrupted DataSyncStatus = "interrupted"
)

func (s DataSyncStatus) Terminal() bool {
	return s == DataSyncSucceeded || s == DataSyncFailed || s == DataSyncInterrupted
}

// DataSyncJob separates immutable admission from mutable, revisioned observations.
// No credentials, raw output, arbitrary exception text or business rows belong here.
type DataSyncJob struct {
	SchemaVersion         int                  `bson:"schema_version"`
	JobID                 string               `bson:"job_id"`
	SourceID              string               `bson:"source_id"`
	OperationKind         string               `bson:"operation_kind,omitempty"`
	InitiatingOwnerID     primitive.ObjectID   `bson:"initiating_owner_id,omitempty"`
	InitiatingChatID      primitive.ObjectID   `bson:"initiating_chat_id,omitempty"`
	RequestID             string               `bson:"request_id"`
	RequestFingerprint    string               `bson:"request_fingerprint"`
	RunnerVersion         string               `bson:"runner_version"`
	Mode                  string               `bson:"mode"`
	ComponentManifestRef  string               `bson:"component_manifest_ref"`
	ComponentDigest       string               `bson:"component_digest"`
	BridgeVersion         string               `bson:"bridge_version"`
	ConfigRevision        int64                `bson:"config_revision"`
	ScopeDigest           string               `bson:"scope_digest"`
	ExpectedShops         []string             `bson:"expected_shops"`
	SourceMapping         map[string]string    `bson:"source_mapping"`
	AftersaleSource       string               `bson:"aftersale_source"`
	InstanceID            string               `bson:"instance_id"`
	OperationID           string               `bson:"operation_id"`
	BindingRevision       int64                `bson:"binding_revision,omitempty"`
	ConnectionFingerprint string               `bson:"connection_fingerprint,omitempty"`
	Status                DataSyncStatus       `bson:"status"`
	Stage                 string               `bson:"stage"`
	Revision              int64                `bson:"revision"`
	CreatedAt             time.Time            `bson:"created_at"`
	StartedAt             *time.Time           `bson:"started_at,omitempty"`
	FinishedAt            *time.Time           `bson:"finished_at,omitempty"`
	Cutoff                *time.Time           `bson:"cutoff,omitempty"`
	ExpectedStates        []ERPSyncState       `bson:"expected_states"`
	ShopResults           []ERPSyncShopResult  `bson:"shop_results"`
	EventSeq              int64                `bson:"event_seq"`
	RunFinished           bool                 `bson:"run_finished"`
	Verification          *ERPSyncVerification `bson:"verification_result,omitempty"`
	SafeErrorCode         string               `bson:"safe_error_code"`
	SafeSummary           string               `bson:"safe_summary"`
	PartialCommits        string               `bson:"partial_commits"`
	PendingOutcome        DataSyncStatus       `bson:"pending_outcome"`
	Process               *ERPSyncProcess      `bson:"process,omitempty"`
	ExitConfirmed         bool                 `bson:"exit_confirmed"`
	ExitCode              *int                 `bson:"exit_code,omitempty"`
	ReleaseConfirmed      bool                 `bson:"release_confirmed"`
}

type ERPSyncProcess struct {
	PID                 int    `bson:"pid"`
	PGID                int    `bson:"pgid"`
	BootID              string `bson:"boot_id"`
	StartTicks          uint64 `bson:"start_ticks"`
	ExecutableSHA256    string `bson:"executable_sha256"`
	MountNamespace      string `bson:"mount_namespace"`
	PIDNamespace        string `bson:"pid_namespace"`
	SourceID            string `bson:"source_id"`
	OperationID         string `bson:"operation_id"`
	StartFrameConfirmed bool   `bson:"start_frame_confirmed"`
}

type ERPSyncState struct {
	Source          string    `bson:"source" json:"source"`
	Entity          string    `bson:"entity" json:"entity"`
	ShopID          string    `bson:"shop_id" json:"shop_id"`
	BeforeWatermark time.Time `bson:"before_watermark" json:"before_watermark"`
}

type ERPSyncShopResult struct {
	ShopID                        string `bson:"shop_id" json:"shop_id"`
	Status                        string `bson:"status" json:"status"`
	OrderSource                   string `bson:"order_source" json:"order_source"`
	OrdersCount                   int64  `bson:"orders_count" json:"orders_count"`
	AftersalesOccurrenceCount     int64  `bson:"aftersales_occurrence_count" json:"aftersales_occurrence_count"`
	RelatedOrdersRefetchCompleted bool   `bson:"related_orders_refetch_completed" json:"related_orders_refetch_completed"`
	SafeErrorCode                 string `bson:"safe_error_code" json:"safe_error_code"`
}

type ERPSyncVerificationRow struct {
	Source          string     `bson:"source" json:"source"`
	Entity          string     `bson:"entity" json:"entity"`
	ShopID          string     `bson:"shop_id" json:"shop_id"`
	BeforeWatermark time.Time  `bson:"before_watermark" json:"before_watermark"`
	Present         bool       `bson:"present" json:"present"`
	Watermark       *time.Time `bson:"watermark,omitempty" json:"watermark"`
	LastErrorCode   *string    `bson:"last_error_code,omitempty" json:"-"`
	LastSuccessAt   *time.Time `bson:"last_success_at,omitempty" json:"last_success_at"`
	Verified        bool       `bson:"verified" json:"verified"`
}

type ERPSyncVerification struct {
	Contract   string                   `bson:"contract" json:"contract"`
	ObservedAt time.Time                `bson:"observed_at" json:"observed_at"`
	Passed     bool                     `bson:"passed" json:"passed"`
	Rows       []ERPSyncVerificationRow `bson:"rows" json:"rows"`
}

func CanonicalSyncUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

// Missing kind is accepted only for the original complete user-sync identity.
// Maintenance never borrows a user, Chat or binding identity.
func (j *DataSyncJob) Kind() string {
	if j == nil {
		return ""
	}
	switch j.OperationKind {
	case "", DataSyncOperation:
		if !j.InitiatingOwnerID.IsZero() && !j.InitiatingChatID.IsZero() && j.BindingRevision > 0 && len(j.ConnectionFingerprint) == 64 {
			return DataSyncOperation
		}
	case ExternalMaintenanceOperation:
		if j.InitiatingOwnerID.IsZero() && j.InitiatingChatID.IsZero() && j.BindingRevision == 0 && j.ConnectionFingerprint == "" {
			return ExternalMaintenanceOperation
		}
	}
	return ""
}

func (j *DataSyncJob) MatchesOperation(op *ERPSourceOperation) bool {
	return j != nil && j.Kind() != "" && op != nil && op.Kind == j.Kind() && op.OperationID == j.OperationID && op.JobID == j.JobID && op.RequestID == j.RequestID && op.OwnerID == j.InitiatingOwnerID && op.ChatID == j.InitiatingChatID
}

// BSON datetimes truncate PostgreSQL microseconds. Evidence watermarks use an
// exact RFC3339Nano representation; JSON/SQL still consume actual time values.
type syncStateBSON struct {
	Source string `bson:"source"`
	Entity string `bson:"entity"`
	ShopID string `bson:"shop_id"`
	Before string `bson:"before_watermark"`
}

func (s ERPSyncState) MarshalBSON() ([]byte, error) {
	return bson.Marshal(syncStateBSON{s.Source, s.Entity, s.ShopID, s.BeforeWatermark.Format(time.RFC3339Nano)})
}
func (s *ERPSyncState) UnmarshalBSON(raw []byte) error {
	var wire syncStateBSON
	if err := bson.Unmarshal(raw, &wire); err != nil {
		return err
	}
	before, err := time.Parse(time.RFC3339Nano, wire.Before)
	if err != nil {
		return err
	}
	*s = ERPSyncState{Source: wire.Source, Entity: wire.Entity, ShopID: wire.ShopID, BeforeWatermark: before}
	return nil
}

type syncVerificationRowBSON struct {
	Source        string  `bson:"source"`
	Entity        string  `bson:"entity"`
	ShopID        string  `bson:"shop_id"`
	Before        string  `bson:"before_watermark"`
	Present       bool    `bson:"present"`
	Watermark     *string `bson:"watermark"`
	LastErrorCode *string `bson:"last_error_code"`
	LastSuccessAt *string `bson:"last_success_at"`
	Verified      bool    `bson:"verified"`
}

func syncExactTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	text := value.Format(time.RFC3339Nano)
	return &text
}
func (r ERPSyncVerificationRow) MarshalBSON() ([]byte, error) {
	return bson.Marshal(syncVerificationRowBSON{r.Source, r.Entity, r.ShopID, r.BeforeWatermark.Format(time.RFC3339Nano), r.Present, syncExactTime(r.Watermark), r.LastErrorCode, syncExactTime(r.LastSuccessAt), r.Verified})
}
func (r *ERPSyncVerificationRow) UnmarshalBSON(raw []byte) error {
	var wire syncVerificationRowBSON
	if err := bson.Unmarshal(raw, &wire); err != nil {
		return err
	}
	before, err := time.Parse(time.RFC3339Nano, wire.Before)
	if err != nil {
		return err
	}
	*r = ERPSyncVerificationRow{Source: wire.Source, Entity: wire.Entity, ShopID: wire.ShopID, BeforeWatermark: before, Present: wire.Present, LastErrorCode: wire.LastErrorCode, Verified: wire.Verified}
	if wire.Watermark != nil {
		value, err := time.Parse(time.RFC3339Nano, *wire.Watermark)
		if err != nil {
			return err
		}
		r.Watermark = &value
	}
	if wire.LastSuccessAt != nil {
		value, err := time.Parse(time.RFC3339Nano, *wire.LastSuccessAt)
		if err != nil {
			return err
		}
		r.LastSuccessAt = &value
	}
	return nil
}
