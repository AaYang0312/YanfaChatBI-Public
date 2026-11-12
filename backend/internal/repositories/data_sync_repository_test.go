package repositories

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"yanfachatbi/internal/models"
)

func dataSyncRepositoryJob() *models.DataSyncJob {
	return &models.DataSyncJob{SchemaVersion: 1, JobID: uuid.NewString(), SourceID: uuid.NewString(), InitiatingOwnerID: primitive.NewObjectID(), InitiatingChatID: primitive.NewObjectID(), RequestID: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64), RunnerVersion: models.DataSyncRunnerVersion, Mode: "incremental", ComponentManifestRef: "test-only-component", ComponentDigest: strings.Repeat("b", 64), BridgeVersion: "test-probe/1", ConfigRevision: 1, ScopeDigest: strings.Repeat("c", 64), ExpectedShops: []string{"1", "2", "3", "4", "5", "6", "7"}, SourceMapping: map[string]string{"1": "orders-a", "2": "orders-a", "3": "orders-a", "4": "orders-a", "5": "orders-a", "6": "orders-a", "7": "orders-a"}, AftersaleSource: "aftersales-a", InstanceID: "old-instance", OperationID: uuid.NewString(), BindingRevision: 1, ConnectionFingerprint: strings.Repeat("d", 64), Status: models.DataSyncCreated, Stage: "preparing", CreatedAt: time.Now().UTC()}
}

func TestDataSyncRepositoryUniqueRequestAndImmutableCAS(t *testing.T) {
	database, _ := erpSourceMongo(t)
	repo := NewDataSyncRepository(database)
	ctx := context.Background()
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	job := dataSyncRepositoryJob()
	var inserted atomic.Int32
	var work sync.WaitGroup
	for range 12 {
		work.Add(1)
		go func() {
			defer work.Done()
			candidate := *job
			candidate.JobID = uuid.NewString()
			if err := repo.Create(ctx, &candidate); err == nil {
				inserted.Add(1)
			} else if !mongo.IsDuplicateKeyError(err) {
				t.Error(err)
			}
		}()
	}
	work.Wait()
	if inserted.Load() != 1 {
		t.Fatalf("durable winners=%d", inserted.Load())
	}
	stored, err := repo.FindRequest(ctx, job.InitiatingOwnerID, job.SourceID, job.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindRequest(ctx, primitive.NewObjectID(), job.SourceID, job.RequestID); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatalf("owner request isolation: %v", err)
	}
	next := *stored
	next.Revision++
	next.Status = models.DataSyncRunning
	next.SourceID = uuid.NewString()
	next.InstanceID = "forged"
	next.RequestID = uuid.NewString()
	if err := repo.Advance(ctx, stored, &next); err != nil {
		t.Fatal(err)
	}
	current, err := repo.Get(ctx, stored.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if current.SourceID != stored.SourceID || current.InstanceID != stored.InstanceID || current.RequestID != stored.RequestID {
		t.Fatal("observation replaced immutable admission")
	}
	if err := repo.Advance(ctx, stored, &next); !errors.Is(err, ErrDataSyncConflict) {
		t.Fatalf("stale callback: %v", err)
	}
	terminal := *current
	terminal.Revision++
	terminal.Status = models.DataSyncInterrupted
	terminal.PendingOutcome = terminal.Status
	terminal.ExitConfirmed = true
	now := time.Now()
	terminal.FinishedAt = &now
	if err := repo.Advance(ctx, current, &terminal); err != nil {
		t.Fatal(err)
	}
	late := terminal
	late.Revision++
	late.Status = models.DataSyncRunning
	if err := repo.Advance(ctx, &terminal, &late); !errors.Is(err, ErrDataSyncInvalid) {
		t.Fatalf("terminal overwritten: %v", err)
	}
	if err := repo.ConfirmRelease(ctx, &terminal); err == nil {
		t.Fatal("terminal alone published release")
	}
}

func TestDataSyncRepositoryExactWatermarksAndFenceReconstruction(t *testing.T) {
	database, sources := erpSourceMongo(t)
	repo := NewDataSyncRepository(database)
	ctx := context.Background()
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	source := erpSourceRegistration(t, strings.Repeat("e", 64))
	if err := sources.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	job := dataSyncRepositoryJob()
	job.SourceID = source.SourceID
	before := time.Date(2026, 11, 7, 1, 2, 3, 123456000, time.UTC)
	job.ExpectedStates = []models.ERPSyncState{{Source: "orders-a", Entity: "orders", ShopID: "1", BeforeWatermark: before}}
	op := &models.ERPSourceOperation{OperationID: job.OperationID, Kind: job.Kind(), JobID: job.JobID, RequestID: job.RequestID, OwnerID: job.InitiatingOwnerID, ChatID: job.InitiatingChatID}
	if err := sources.UpdateStateCAS(ctx, source.SourceID, 0, "", ERPSourceState{Phase: models.ERPSourceSyncing, ActiveOperation: op, FrozenJob: job}); err != nil {
		t.Fatal(err)
	}
	if err := sources.UpdateStateCAS(ctx, source.SourceID, 1, op.OperationID, ERPSourceState{Phase: models.ERPSourceRecovering, ActiveOperation: op}); err != nil {
		t.Fatal(err)
	}
	frozen, err := sources.(SyncFenceStore).FrozenSyncJob(ctx, source.SourceID, op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if !frozen.ExpectedStates[0].BeforeWatermark.Equal(before) {
		t.Fatalf("PG microseconds changed: %s", frozen.ExpectedStates[0].BeforeWatermark)
	}
	if err := repo.Create(ctx, frozen); err != nil {
		t.Fatal(err)
	}
	restarted := NewDataSyncRepository(database)
	stored, err := restarted.Get(ctx, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RequestID != job.RequestID || stored.OperationID != op.OperationID || !stored.ExpectedStates[0].BeforeWatermark.Equal(before) {
		t.Fatal("restart re-froze admission or truncated evidence")
	}
}

func TestDataSyncMaintenanceIdentityAndPublicHistory(t *testing.T) {
	database, sources := erpSourceMongo(t)
	repo := NewDataSyncRepository(database)
	ctx := context.Background()
	if repo.EnsureIndexes(ctx) != nil {
		t.Fatal("indexes")
	}
	source := erpSourceRegistration(t, strings.Repeat("9", 64))
	if sources.Create(ctx, source) != nil {
		t.Fatal("source fixture")
	}
	maintenance := dataSyncRepositoryJob()
	maintenance.SourceID = source.SourceID
	maintenance.OperationKind = models.ExternalMaintenanceOperation
	maintenance.InitiatingOwnerID, maintenance.InitiatingChatID = primitive.NilObjectID, primitive.NilObjectID
	maintenance.BindingRevision = 0
	maintenance.ConnectionFingerprint = ""
	if err := repo.Create(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindRequest(ctx, primitive.NewObjectID(), source.SourceID, maintenance.RequestID); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatal("maintenance matched a user request")
	}
	read, err := repo.FindRequest(ctx, primitive.NilObjectID, source.SourceID, maintenance.RequestID)
	if err != nil || read.Kind() != models.ExternalMaintenanceOperation {
		t.Fatal("maintenance request namespace lost")
	}
	corrupt := *maintenance
	corrupt.JobID = uuid.NewString()
	corrupt.RequestID = uuid.NewString()
	corrupt.InitiatingOwnerID = primitive.NewObjectID()
	if repo.Create(ctx, &corrupt) == nil {
		t.Fatal("maintenance accepted user identity")
	}
	unknown := *maintenance
	unknown.JobID = uuid.NewString()
	unknown.RequestID = uuid.NewString()
	unknown.OperationKind = "unknown"
	if repo.Create(ctx, &unknown) == nil {
		t.Fatal("unknown operation kind accepted")
	}
	legacy := dataSyncRepositoryJob()
	legacy.SourceID = source.SourceID
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatal("legal missing-kind sync rejected", err)
	}
	jobs, err := repo.ListUnreleased(ctx, source.SourceID)
	if err != nil || len(jobs) != 2 {
		t.Fatal("mixed predecessor set incomplete")
	}
	if ConfirmSyncPredecessors(ctx, repo, source, jobs) == nil {
		t.Fatal("mixed active predecessors were published")
	}
	next := *maintenance
	next.Revision++
	next.Status = models.DataSyncRunning
	next.OperationKind = models.DataSyncOperation
	if repo.Advance(ctx, maintenance, &next) == nil {
		t.Fatal("maintenance kind changed in event CAS")
	}
	// Publish a real terminal source witness, then prove even a later maintenance
	// success is excluded from the public user-success query.
	cutoff := time.Now()
	zero := 0
	next = *maintenance
	next.Revision++
	next.Status = models.DataSyncSucceeded
	next.Stage = "verifying"
	next.PendingOutcome = models.DataSyncSucceeded
	next.ExitConfirmed = true
	next.ExitCode = &zero
	next.RunFinished = true
	next.Cutoff = &cutoff
	next.FinishedAt = &cutoff
	next.Verification = &models.ERPSyncVerification{Passed: true}
	if repo.Advance(ctx, maintenance, &next) != nil {
		t.Fatal("maintenance terminal CAS")
	}
	if sources.UpdateStateCAS(ctx, source.SourceID, 0, "", ERPSourceState{Phase: models.ERPSourceIdle, LastReleasedOperationID: maintenance.OperationID, RecoveryVerifiedInstanceID: "repository-fixture"}) != nil {
		t.Fatal("release witness")
	}
	if repo.ConfirmRelease(ctx, &next) != nil {
		t.Fatal("maintenance release")
	}
	if _, err := repo.LatestSuccess(ctx, source.SourceID); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatal("maintenance appeared in user success history")
	}
}
