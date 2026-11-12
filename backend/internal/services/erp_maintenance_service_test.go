package services

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/erpmaintenance"
	"yanfachatbi/pkg/sourcegate"
)

func maintenanceJob(t *testing.T, w *dataSyncWorld, request string, released bool) *models.DataSyncJob {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, err := w.jobs.FindRequest(context.Background(), primitive.NilObjectID, w.source.SourceID, request)
		if err == nil && (!released || job.ReleaseConfirmed) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("maintenance durable result not observed")
	return nil
}
func TestMaintenanceSharesLifecycleWithoutUserJob(t *testing.T) {
	w := newDataSyncWorld(t, "ok")
	if err := w.service.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	read, err := w.f.gate.Acquire(context.Background(), sourceTarget(w.source), sourcegate.Shared, sourcegate.Identity{SourceID: w.source.SourceID, OperationID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.admitMaintenance(context.Background(), w.source.SourceID, uuid.NewString()); err == nil {
		t.Fatal("maintenance crossed existing read")
	}
	read.Close()
	barrier := &dataSyncFaultStore{DataSyncRepository: w.jobs, identityStored: make(chan struct{}), identityRelease: make(chan struct{})}
	w.service.jobs = barrier
	defer func() {
		select {
		case <-barrier.identityRelease:
		default:
			close(barrier.identityRelease)
		}
	}()
	request := uuid.NewString()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(w.f.service.lockDirectory, "maintenance.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if erpmaintenance.Encode(conn, erpmaintenance.Request{Protocol: erpmaintenance.Protocol, SourceID: w.source.SourceID, RequestID: request, Operation: "incremental"}) != nil {
		t.Fatal("request write")
	}
	conn.CloseWrite()
	conn.Close()
	select {
	case <-barrier.identityStored:
	case <-time.After(10 * time.Second):
		t.Fatal("disconnected request did not persist")
	}
	job := maintenanceJob(t, w, request, false)
	if job.Kind() != models.ExternalMaintenanceOperation || !job.InitiatingOwnerID.IsZero() || !job.InitiatingChatID.IsZero() || job.BindingRevision != 0 || job.ConnectionFingerprint != "" {
		t.Fatal("maintenance fabricated a user identity")
	}
	status, err := w.service.Status(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
	if err != nil || status.ActiveJob != nil || status.MatchedJob != nil || status.RequestMatch != "not_found" || status.OperationKind != models.ExternalMaintenanceOperation || status.MaintenanceOperationRef != job.OperationID || status.AskAllowed || status.CanStart {
		t.Fatal("public maintenance state impersonated a user job")
	}
	if _, err := w.service.GetJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), job.JobID); err == nil {
		t.Fatal("maintenance exposed as public job")
	}
	if _, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), uuid.NewString()); err == nil {
		t.Fatal("app sync crossed maintenance")
	}
	probe, e := w.f.gate.Acquire(context.Background(), sourceTarget(w.source), sourcegate.Shared, sourcegate.Identity{SourceID: w.source.SourceID, OperationID: uuid.NewString()})
	if probe != nil {
		probe.Close()
	}
	if !errors.Is(e, sourcegate.ErrBusy) {
		t.Fatal("disconnected maintenance released EX")
	}
	close(barrier.identityRelease)
	job = maintenanceJob(t, w, request, true)
	if job.Status != models.DataSyncSucceeded || w.runner.starts.Load() != 1 {
		t.Fatal("maintenance did not use one actual writer")
	}
	status, err = w.service.Status(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), "")
	if err != nil || status.LatestSuccessfulSyncAt != nil || !status.AskAllowed {
		t.Fatal("maintenance changed user last success or release")
	}
	ref, err := w.service.admitMaintenance(context.Background(), w.source.SourceID, request)
	if err != nil || ref != job.OperationID || w.runner.starts.Load() != 1 {
		t.Fatal("explicit same-ID replay reran maintenance")
	}
}
func TestMaintenanceFrozenInsertFailureRecoversWithoutExecution(t *testing.T) {
	w := newDataSyncWorld(t, "ok")
	w.service.jobs = &dataSyncFaultStore{DataSyncRepository: w.jobs, disconnected: dataSyncDisconnected(t, w), failCreate: true}
	request := uuid.NewString()
	if _, err := w.service.admitMaintenance(context.Background(), w.source.SourceID, request); err == nil {
		t.Fatal("lost record was accepted")
	}
	if w.runner.starts.Load() != 0 {
		t.Fatal("writer started before durable documents")
	}
	w.service.Shutdown(context.Background())
	restarted := NewDeploymentDataSyncService(w.f.service, w.jobs)
	if err := restarted.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(context.Background())
	job := maintenanceJob(t, w, request, true)
	if job.Kind() != models.ExternalMaintenanceOperation || job.Status != models.DataSyncInterrupted || job.Process != nil {
		t.Fatal("frozen maintenance was rerun or not recovered")
	}
}
