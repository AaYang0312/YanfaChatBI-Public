package dtos

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
)

type DataSyncReadiness struct {
	State   string   `json:"state"`
	Reasons []string `json:"reasons"`
}

type DataSyncStatusResponse struct {
	SchemaVersion           int               `json:"schema_version"`
	SourceID                string            `json:"source_id"`
	SourceRevision          int64             `json:"source_revision"`
	BindingRevision         int64             `json:"binding_revision"`
	DisplayName             string            `json:"display_name"`
	Readiness               DataSyncReadiness `json:"readiness"`
	CanStart                bool              `json:"can_start"`
	AskAllowed              bool              `json:"ask_allowed"`
	BlockReason             string            `json:"block_reason"`
	MaintenanceState        string            `json:"maintenance_state"`
	OperationKind           string            `json:"operation_kind,omitempty"`
	MaintenanceOperationRef string            `json:"maintenance_operation_ref,omitempty"`
	ActiveJob               *DataSyncJobView  `json:"active_job"`
	LatestSuccessfulSyncAt  *time.Time        `json:"latest_successful_sync_at"`
	RequestMatch            string            `json:"request_match,omitempty"`
	MatchedJob              *DataSyncJobView  `json:"matched_job,omitempty"`
}

type DataSyncJobView struct {
	SchemaVersion      int                        `json:"schema_version"`
	JobID              string                     `json:"job_id"`
	SourceID           string                     `json:"source_id"`
	RequestID          string                     `json:"request_id,omitempty"`
	InitiatingChatID   string                     `json:"initiating_chat_id,omitempty"`
	Status             models.DataSyncStatus      `json:"status"`
	Stage              string                     `json:"stage"`
	Revision           int64                      `json:"revision"`
	CreatedAt          time.Time                  `json:"created_at"`
	StartedAt          *time.Time                 `json:"started_at"`
	FinishedAt         *time.Time                 `json:"finished_at"`
	Cutoff             *time.Time                 `json:"cutoff"`
	ExpectedCount      *int                       `json:"expected_count"`
	ProcessedCount     int                        `json:"processed_count"`
	SucceededCount     int                        `json:"succeeded_count"`
	FailedCount        int                        `json:"failed_count"`
	ShopResults        []models.ERPSyncShopResult `json:"shop_results"`
	VerificationPassed bool                       `json:"verification_passed"`
	SafeErrorCode      string                     `json:"safe_error_code"`
	SafeSummary        string                     `json:"safe_summary"`
	PartialCommits     string                     `json:"partial_commits"`
	ReleaseConfirmed   bool                       `json:"release_confirmed"`
}

func NewDataSyncJobView(job *models.DataSyncJob, viewer primitive.ObjectID) *DataSyncJobView {
	if job == nil || job.Kind() != models.DataSyncOperation {
		return nil
	}
	view := &DataSyncJobView{SchemaVersion: 1, JobID: job.JobID, SourceID: job.SourceID, Status: job.Status, Stage: job.Stage, Revision: job.Revision, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, Cutoff: job.Cutoff, ShopResults: job.ShopResults, SafeErrorCode: job.SafeErrorCode, SafeSummary: job.SafeSummary, PartialCommits: job.PartialCommits, ReleaseConfirmed: job.ReleaseConfirmed}
	if job.InitiatingOwnerID == viewer {
		view.RequestID = job.RequestID
		view.InitiatingChatID = job.InitiatingChatID.Hex()
	}
	if job.Cutoff != nil {
		count := len(job.ExpectedShops)
		view.ExpectedCount = &count
	}
	for _, shop := range job.ShopResults {
		view.ProcessedCount++
		if shop.Status == "ok" {
			view.SucceededCount++
		} else {
			view.FailedCount++
		}
	}
	view.VerificationPassed = job.Verification != nil && job.Verification.Passed
	if !job.ReleaseConfirmed && job.Status.Terminal() {
		view.Status = models.DataSyncRunning
		view.Stage = "verifying"
		view.FinishedAt = nil
		view.SafeErrorCode = "DATA_SYNC_RECOVERING"
		view.SafeSummary = "同步结果正在恢复收尾"
	}
	return view
}
