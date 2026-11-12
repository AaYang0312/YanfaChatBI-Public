package services

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/erpmaintenance"
	"yanfachatbi/pkg/sourcegate"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// startMaintenance is called only after this service's Startup recovery. The
// backend.instance claim excludes a live predecessor before a stale socket is removed.
func (s *DataSyncService) startMaintenance() error {
	if s.access.repo == nil {
		return nil
	}
	if s.instanceFile == nil || erpmaintenance.CheckDirectory(s.access.lockDirectory, s.access.uid) != nil {
		return errSyncProcess
	}
	path := filepath.Join(s.access.lockDirectory, "maintenance.sock")
	if _, err := os.Lstat(path); err == nil {
		if erpmaintenance.CheckSocket(path, s.access.uid) != nil {
			return errSyncProcess
		}
		if os.Remove(path) != nil {
			return errSyncProcess
		}
	} else if !os.IsNotExist(err) {
		return errSyncProcess
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return errSyncProcess
	}
	listener.SetUnlinkOnClose(false)
	if os.Chmod(path, 0600) != nil || erpmaintenance.CheckSocket(path, s.access.uid) != nil {
		listener.Close()
		return errSyncProcess
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		listener.Close()
		return errSyncProcess
	}
	s.maintenance = listener
	s.work.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.work.Done()
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			if !erpmaintenance.SameUID(conn, s.access.uid) || !s.begin() {
				conn.Close()
				continue
			}
			go func() { defer s.work.Done(); defer conn.Close(); s.serveMaintenance(conn) }()
		}
	}()
	return nil
}
func (s *DataSyncService) serveMaintenance(conn *net.UnixConn) {
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	var request erpmaintenance.Request
	if erpmaintenance.Decode(conn, &request) != nil {
		return
	}
	// The client connection does not own the admission or execution cancellation.
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	ref, err := s.admitMaintenance(ctx, request.SourceID, request.RequestID)
	response := erpmaintenance.Response{Protocol: erpmaintenance.Protocol, RequestID: request.RequestID, OperationRef: ref, Code: "ACCEPTED"}
	if err != nil {
		response.Code = "SYNC_STATE_UNAVAILABLE"
		var safe *DataSyncError
		if errors.As(err, &safe) {
			response.Code = safe.Code
		}
	}
	_ = erpmaintenance.Encode(conn, response)
}

func (s *DataSyncService) maintenanceRequest(ctx context.Context, source, request string) (*models.DataSyncJob, error) {
	job, err := s.jobs.FindRequest(ctx, primitive.NilObjectID, source, request)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if job.Kind() != models.ExternalMaintenanceOperation || job.SourceID != source || job.RequestID != request {
		return nil, syncError(409, "REQUEST_CONFLICT")
	}
	return job, nil
}
func (s *DataSyncService) admitMaintenance(ctx context.Context, sourceID, request string) (string, error) {
	if !models.CanonicalSyncUUID(sourceID) || !models.CanonicalSyncUUID(request) {
		return "", syncError(400, "INVALID_REQUEST")
	}
	if !s.begin() {
		return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	defer s.work.Done()
	source, err := s.access.source(ctx, sourceID)
	if err != nil {
		return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if old, err := s.maintenanceRequest(ctx, sourceID, request); old != nil || err != nil {
		if old != nil {
			return old.OperationID, nil
		}
		return "", err
	}
	if readyErr := s.access.ready(source); readyErr != nil {
		if errors.Is(readyErr, ErrSourceSyncing) {
			return "", syncError(423, "DATA_SYNC_IN_PROGRESS")
		}
		return "", syncError(423, "DATA_SYNC_RECOVERING")
	}
	digest, bridge, err := s.runner.Ready(source)
	if err != nil {
		return "", syncError(409, "SYNC_NOT_READY")
	}
	job, err := freezeSyncJob(source, nil, request, s.access.instanceID, digest, bridge, s.access.syncRuntime.Sources[sourceID].AftersaleSource)
	if err != nil {
		return "", err
	}
	lease, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, jobSyncIdentity(job))
	if errors.Is(err, sourcegate.ErrBusy) {
		if old, readErr := s.maintenanceRequest(ctx, sourceID, request); old != nil || readErr != nil {
			if old != nil {
				return old.OperationID, nil
			}
			return "", readErr
		}
		fresh, readErr := s.access.source(ctx, sourceID)
		if readErr != nil {
			return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
		}
		if fresh.ActiveOperation != nil {
			return "", syncError(423, "DATA_SYNC_IN_PROGRESS")
		}
		probe, e := s.access.gate.Acquire(ctx, sourceTarget(fresh), sourcegate.Shared, jobSyncIdentity(job))
		if e == nil {
			probe.Close()
			return "", syncError(409, "SOURCE_BUSY")
		}
		return "", syncError(423, "DATA_SYNC_RECOVERING")
	}
	if err != nil {
		return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	transferred := false
	defer func() {
		if !transferred {
			lease.Close()
		}
	}()
	fresh, err := s.access.source(ctx, sourceID)
	if err != nil || fresh.ConfigRevision != source.ConfigRevision {
		return "", syncError(409, "REQUEST_CONFLICT")
	}
	if old, err := s.maintenanceRequest(ctx, sourceID, request); old != nil || err != nil {
		if old != nil {
			return old.OperationID, nil
		}
		return "", err
	}
	if s.access.ready(fresh) != nil {
		return "", syncError(423, "DATA_SYNC_RECOVERING")
	}
	source = fresh
	unreleased, err := s.jobs.ListUnreleased(ctx, sourceID)
	if err != nil || repositories.ConfirmSyncPredecessors(ctx, s.jobs, source, unreleased) != nil {
		return "", syncError(423, "DATA_SYNC_RECOVERING")
	}
	// Prepare consumes the same immutable manifest and its closed maintenance allow-list.
	launch, err := s.runner.Prepare(ctx, source, job)
	if err != nil {
		return "", syncError(409, "SYNC_NOT_READY")
	}
	if s.verifier.Observe(ctx, source, job, lease) != nil {
		s.markRecovering(source)
		return "", syncError(423, "DATA_SYNC_RECOVERING")
	}
	states, err := s.verifier.Preflight(ctx, source, job, lease)
	if err != nil {
		return "", syncError(409, "SYNC_NOT_READY")
	}
	job.ExpectedStates = states
	boot, err := syncBootID()
	if err != nil {
		return "", syncError(409, "SYNC_NOT_READY")
	}
	if s.access.repo.UpdateStateCAS(ctx, sourceID, source.StateRevision, "", repositories.ERPSourceState{Phase: models.ERPSourceSyncing, ActiveOperation: syncOperation(job, boot), FrozenJob: job, RecoveryVerifiedInstanceID: s.access.instanceID, LastReleasedOperationID: source.LastReleasedOperationID}) != nil {
		s.markRecovering(source)
		return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	fenced, err := s.access.source(ctx, sourceID)
	if err != nil || !job.MatchesOperation(fenced.ActiveOperation) || fenced.ConfigRevision != job.ConfigRevision {
		s.markRecovering(source)
		return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	source = fenced
	if s.jobs.Create(ctx, job) != nil {
		s.markRecovering(source)
		return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	stored, err := s.jobs.Get(ctx, job.JobID)
	if err != nil || stored.Kind() != models.ExternalMaintenanceOperation || stored.RequestFingerprint != job.RequestFingerprint || stored.Revision != 0 || stored.Status != models.DataSyncCreated {
		s.markRecovering(source)
		return "", syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	s.work.Add(1)
	transferred = true
	go func() { defer s.work.Done(); s.execute(source, stored, lease, launch) }()
	return stored.OperationID, nil
}
