package repositories

import (
	"context"
	"errors"

	"yanfachatbi/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

var ErrDataSyncConflict = errors.New("DATA_SYNC_CAS_CONFLICT")
var ErrDataSyncInvalid = errors.New("DATA_SYNC_INVALID")

type DataSyncRepository interface {
	EnsureIndexes(context.Context) error
	Create(context.Context, *models.DataSyncJob) error
	Get(context.Context, string) (*models.DataSyncJob, error)
	FindRequest(context.Context, primitive.ObjectID, string, string) (*models.DataSyncJob, error)
	ListUnreleased(context.Context, string) ([]models.DataSyncJob, error)
	LatestSuccess(context.Context, string) (*models.DataSyncJob, error)
	Advance(context.Context, *models.DataSyncJob, *models.DataSyncJob) error
	ConfirmRelease(context.Context, *models.DataSyncJob) error
}

type mongoDataSyncRepository struct{ jobs, sources *mongo.Collection }

func NewDataSyncRepository(database *mongo.Database) DataSyncRepository {
	opts := options.Collection().SetReadPreference(readpref.Primary()).SetReadConcern(readconcern.Majority()).SetWriteConcern(writeconcern.Majority())
	return &mongoDataSyncRepository{jobs: database.Collection("data_sync_jobs", opts), sources: database.Collection("erp_sources", opts)}
}

func (r *mongoDataSyncRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.jobs.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "job_id", Value: 1}}, Options: options.Index().SetUnique(true).SetName("data_sync_job_id")},
		{Keys: bson.D{{Key: "initiating_owner_id", Value: 1}, {Key: "source_id", Value: 1}, {Key: "request_id", Value: 1}}, Options: options.Index().SetUnique(true).SetName("data_sync_request")},
		{Keys: bson.D{{Key: "source_id", Value: 1}, {Key: "release_confirmed", Value: 1}}},
		{Keys: bson.D{{Key: "source_id", Value: 1}, {Key: "operation_kind", Value: 1}, {Key: "status", Value: 1}, {Key: "finished_at", Value: -1}}},
	})
	if err != nil {
		return err
	}
	_, err = r.jobs.Indexes().DropOne(ctx, "source_id_1_status_1_finished_at_-1")
	var missing mongo.CommandError
	if errors.As(err, &missing) && missing.Code == 27 {
		return nil
	}
	return err
}

func validSyncJob(j *models.DataSyncJob) bool {
	if j == nil || j.SchemaVersion != 1 || j.Mode != "incremental" || j.RunnerVersion != models.DataSyncRunnerVersion ||
		!models.CanonicalSyncUUID(j.JobID) || !models.CanonicalSyncUUID(j.SourceID) || !models.CanonicalSyncUUID(j.OperationID) || !models.CanonicalSyncUUID(j.RequestID) ||
		j.Kind() == "" || j.InstanceID == "" || j.ConfigRevision < 1 || j.Revision < 0 ||
		len(j.RequestFingerprint) != 64 || len(j.ScopeDigest) != 64 || len(j.ComponentDigest) != 64 ||
		j.ComponentManifestRef == "" || j.BridgeVersion == "" || j.AftersaleSource == "" || len(j.ExpectedShops) != 7 || len(j.SourceMapping) != 7 || j.CreatedAt.IsZero() {
		return false
	}
	seen := make(map[string]bool, 7)
	for _, shop := range j.ExpectedShops {
		if shop == "" || seen[shop] || j.SourceMapping[shop] == "" {
			return false
		}
		seen[shop] = true
	}
	return true
}

func (r *mongoDataSyncRepository) Create(ctx context.Context, j *models.DataSyncJob) error {
	if !validSyncJob(j) || j.Status != models.DataSyncCreated || j.Stage != "preparing" || j.Revision != 0 || j.Process != nil || j.EventSeq != 0 || j.ReleaseConfirmed || j.PendingOutcome != "" {
		return ErrDataSyncInvalid
	}
	_, err := r.jobs.InsertOne(ctx, j)
	return err
}

func (r *mongoDataSyncRepository) one(ctx context.Context, filter bson.M, opts ...*options.FindOneOptions) (*models.DataSyncJob, error) {
	var j models.DataSyncJob
	if err := r.jobs.FindOne(ctx, filter, opts...).Decode(&j); err != nil {
		return nil, err
	}
	if !validSyncJob(&j) {
		return nil, ErrDataSyncInvalid
	}
	return &j, nil
}
func (r *mongoDataSyncRepository) Get(ctx context.Context, id string) (*models.DataSyncJob, error) {
	return r.one(ctx, bson.M{"job_id": id})
}
func (r *mongoDataSyncRepository) FindRequest(ctx context.Context, owner primitive.ObjectID, source, request string) (*models.DataSyncJob, error) {
	filter := bson.M{"initiating_owner_id": owner, "source_id": source, "request_id": request}
	if owner.IsZero() {
		filter["initiating_owner_id"] = nil
		filter["operation_kind"] = models.ExternalMaintenanceOperation
	} else {
		filter["operation_kind"] = bson.M{"$in": bson.A{nil, models.DataSyncOperation}}
	}
	return r.one(ctx, filter)
}
func (r *mongoDataSyncRepository) LatestSuccess(ctx context.Context, source string) (*models.DataSyncJob, error) {
	return r.one(ctx, bson.M{"source_id": source, "operation_kind": bson.M{"$in": bson.A{nil, models.DataSyncOperation}}, "status": models.DataSyncSucceeded, "release_confirmed": true}, options.FindOne().SetSort(bson.D{{Key: "finished_at", Value: -1}}))
}
func (r *mongoDataSyncRepository) ListUnreleased(ctx context.Context, source string) ([]models.DataSyncJob, error) {
	cur, err := r.jobs.Find(ctx, bson.M{"source_id": source, "release_confirmed": bson.M{"$ne": true}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var jobs []models.DataSyncJob
	if err := cur.All(ctx, &jobs); err != nil {
		return nil, err
	}
	for i := range jobs {
		if !validSyncJob(&jobs[i]) {
			return nil, ErrDataSyncInvalid
		}
	}
	return jobs, nil
}

// ConfirmSyncPredecessors publishes only terminal jobs matching the current
// release witness. The caller holds this source's EX and supplies primary reads.
func ConfirmSyncPredecessors(ctx context.Context, store DataSyncRepository, source *models.ERPSource, jobs []models.DataSyncJob) error {
	if source == nil {
		return ErrDataSyncInvalid
	}
	for i := range jobs {
		job := &jobs[i]
		if !validSyncJob(job) || job.SourceID != source.SourceID || !job.Status.Terminal() || job.OperationID != source.LastReleasedOperationID {
			return ErrDataSyncConflict
		}
	}
	for i := range jobs {
		if err := store.ConfirmRelease(ctx, &jobs[i]); err != nil {
			return err
		}
	}
	return nil
}

func syncCAS(j *models.DataSyncJob) bson.M {
	filter := bson.M{"job_id": j.JobID, "source_id": j.SourceID, "instance_id": j.InstanceID, "operation_id": j.OperationID, "status": j.Status, "revision": j.Revision}
	if j.Kind() == models.DataSyncOperation {
		filter["operation_kind"] = bson.M{"$in": bson.A{nil, models.DataSyncOperation}}
	} else {
		filter["operation_kind"] = models.ExternalMaintenanceOperation
	}
	return filter
}

// Advance only writes mutable observations. An event cannot replace the frozen
// request, source, owner, config, instance, component or scope of the operation.
func (r *mongoDataSyncRepository) Advance(ctx context.Context, previous, next *models.DataSyncJob) error {
	if !validSyncJob(previous) || previous.Status.Terminal() || !validSyncJob(next) || next.OperationKind != previous.OperationKind || next.JobID != previous.JobID || next.Revision != previous.Revision+1 ||
		(next.Status != models.DataSyncRunning && !next.Status.Terminal()) || (next.Stage != "preparing" && next.Stage != "syncing" && next.Stage != "verifying") || next.EventSeq < previous.EventSeq {
		return ErrDataSyncInvalid
	}
	if previous.Cutoff != nil && (next.Cutoff == nil || !next.Cutoff.Equal(*previous.Cutoff)) {
		return ErrDataSyncInvalid
	}
	if previous.Stage == "verifying" && next.Stage != "verifying" || previous.Stage == "syncing" && next.Stage == "preparing" {
		return ErrDataSyncInvalid
	}
	if previous.Process != nil {
		if next.Process == nil || previous.Process.StartFrameConfirmed && !next.Process.StartFrameConfirmed {
			return ErrDataSyncInvalid
		}
		a, b := *previous.Process, *next.Process
		a.StartFrameConfirmed = b.StartFrameConfirmed
		if a != b {
			return ErrDataSyncInvalid
		}
	}
	if next.Status.Terminal() && (!next.ExitConfirmed || next.FinishedAt == nil || next.PendingOutcome != next.Status) {
		return ErrDataSyncInvalid
	}
	if next.Status == models.DataSyncSucceeded && (next.Verification == nil || !next.Verification.Passed || next.ExitCode == nil || *next.ExitCode != 0 || !next.RunFinished || next.Cutoff == nil || next.SafeErrorCode != "") {
		return ErrDataSyncInvalid
	}
	set := bson.M{"status": next.Status, "stage": next.Stage, "started_at": next.StartedAt, "finished_at": next.FinishedAt,
		"cutoff": next.Cutoff, "shop_results": next.ShopResults, "event_seq": next.EventSeq, "run_finished": next.RunFinished,
		"verification_result": next.Verification, "safe_error_code": next.SafeErrorCode, "safe_summary": next.SafeSummary, "partial_commits": next.PartialCommits,
		"pending_outcome": next.PendingOutcome, "process": next.Process, "exit_confirmed": next.ExitConfirmed, "exit_code": next.ExitCode}
	result, err := r.jobs.UpdateOne(ctx, syncCAS(previous), bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrDataSyncConflict
	}
	return nil
}

// A terminal job alone is never release evidence. Publishing is a separate
// idempotent step after primary/majority observation of the source clear CAS.
func (r *mongoDataSyncRepository) ConfirmRelease(ctx context.Context, j *models.DataSyncJob) error {
	if !validSyncJob(j) || !j.Status.Terminal() {
		return ErrDataSyncInvalid
	}
	if j.ReleaseConfirmed {
		return nil
	}
	var source struct {
		Last string `bson:"last_released_operation_id"`
	}
	if err := r.sources.FindOne(ctx, bson.M{"source_id": j.SourceID, "last_released_operation_id": j.OperationID}).Decode(&source); err != nil {
		return err
	}
	filter := syncCAS(j)
	filter["release_confirmed"] = bson.M{"$ne": true}
	result, err := r.jobs.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"release_confirmed": true}, "$inc": bson.M{"revision": int64(1)}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		current, readErr := r.Get(ctx, j.JobID)
		if readErr != nil || current.OperationID != j.OperationID || current.Status != j.Status || !current.ReleaseConfirmed {
			return ErrDataSyncConflict
		}
		j.Revision = current.Revision
		j.ReleaseConfirmed = true
		return nil
	}
	j.Revision++
	j.ReleaseConfirmed = true
	return nil
}
