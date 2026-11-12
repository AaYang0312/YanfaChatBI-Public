package services

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/sourcegate"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sys/unix"
)

type DataSyncError struct {
	Status                 int
	Code, SourceID, JobRef string
}

func (e *DataSyncError) Error() string { return e.Code }
func SyncErrorMessage(code string) string {
	switch code {
	case "NOT_FOUND":
		return "未找到可访问的数据同步资源"
	case "INVALID_REQUEST":
		return "同步请求格式不正确"
	case "SYNC_FORBIDDEN":
		return "当前账号没有启动同步的权限"
	case "SOURCE_BUSY":
		return "当前有问数任务，请结束后再同步"
	case "DATA_SYNC_IN_PROGRESS":
		return "数据同步或维护正在进行"
	case "DATA_SYNC_RECOVERING":
		return "同步状态正在恢复确认，暂时无法执行"
	case "REQUEST_CONFLICT":
		return "请求标识与原同步操作冲突"
	case "SYNC_NOT_READY":
		return "同步组件、配置或批准前置尚未就绪"
	default:
		return "同步状态暂不可确认"
	}
}
func syncError(status int, code string) error { return &DataSyncError{Status: status, Code: code} }

// DataSyncService has no queue, retry policy, timer-based lock expiry or local
// busy map. goroutine accounting only joins actual work during shutdown.
type DataSyncService struct {
	access       *SourceAccessService
	jobs         repositories.DataSyncRepository
	runner       ERPSyncRunner
	verifier     ERPSyncVerifier
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closing      bool
	work         sync.WaitGroup
	instanceFile *os.File
	maintenance  *net.UnixListener
}

func NewDataSyncService(access *SourceAccessService, jobs repositories.DataSyncRepository, runner ERPSyncRunner, verifier ERPSyncVerifier) *DataSyncService {
	if access == nil || jobs == nil || runner == nil || verifier == nil {
		panic("data sync dependencies are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DataSyncService{access: access, jobs: jobs, runner: runner, verifier: verifier, ctx: ctx, cancel: cancel}
}

func NewDeploymentDataSyncService(access *SourceAccessService, jobs repositories.DataSyncRepository) *DataSyncService {
	return NewDataSyncService(access, jobs, NewERPSyncRunner(access.syncRuntime, access.uid), NewERPSyncVerifier(access.syncRuntime, access.instanceID))
}
func (s *DataSyncService) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.work.Add(1)
	return true
}
func (s *DataSyncService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.cancel()
	if s.maintenance != nil {
		s.maintenance.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.work.Wait(); close(done) }()
	select {
	case <-done:
		if s.instanceFile != nil {
			return s.instanceFile.Close()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Singleton flock checks the explicit single-backend deployment assumption. It
// is not a source busy authority and never substitutes for a source EX/SH.
func (s *DataSyncService) claimInstance() error {
	if s.access.gate == nil {
		return nil
	}
	fd, err := unix.Open(filepath.Join(s.access.lockDirectory, "backend.instance"), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errSyncProcess
	}
	file := os.NewFile(uintptr(fd), "erp-backend-instance")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != s.access.uid || st.Mode&0777 != 0600 || st.Nlink != 1 || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		file.Close()
		return errSyncProcess
	}
	s.instanceFile = file
	return nil
}

// Startup runs before any HTTP route or DB Manager is resolved. A new runtime
// instance identity makes every old recovery_verified_instance_id a veto even
// while Mongo cannot be observed. No recovery branch launches a subprocess.
func (s *DataSyncService) Startup(ctx context.Context) error {
	if err := s.startupSources(ctx); err != nil {
		return err
	}
	return s.startMaintenance()
}

func (s *DataSyncService) startupSources(ctx context.Context) error {
	if s.access.repo != nil {
		if err := s.claimInstance(); err != nil {
			return err
		}
	}
	if err := s.jobs.EnsureIndexes(ctx); err != nil {
		log.Print("Data sync startup storage=unavailable")
		return nil
	}
	if s.access.repo == nil {
		return nil
	}
	if s.access.repo.EnsureIndexes(ctx) != nil {
		log.Print("Data sync startup registry=unavailable")
		return nil
	}
	sources, err := s.access.repo.List(ctx)
	if err != nil {
		log.Print("Data sync startup registry=unavailable")
		return nil
	}
	for i := range sources {
		source := &sources[i]
		_ = s.runner.Load(source) // A missing official release is an explicit readiness denial.
		if err := s.recoverSource(ctx, source); err != nil {
			log.Printf("Data sync source %s recovery=unconfirmed", source.SourceID)
		}
	}
	return nil
}

func (s *DataSyncService) authorize(ctx context.Context, ownerText, chatText string) (*models.Chat, *models.ERPSource, error) {
	owner, err := primitive.ObjectIDFromHex(ownerText)
	if err != nil {
		return nil, nil, syncError(404, "NOT_FOUND")
	}
	chatID, err := primitive.ObjectIDFromHex(chatText)
	if err != nil {
		return nil, nil, syncError(404, "NOT_FOUND")
	}
	if s.access.chats == nil {
		return nil, nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	chat, err := s.access.chats.ReadCurrent(ctx, owner, chatID)
	if errors.Is(err, mongo.ErrNoDocuments) || errors.Is(err, repositories.ErrChatBindingConflict) {
		return nil, nil, syncError(404, "NOT_FOUND")
	}
	if err != nil {
		return nil, nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if chat == nil || chat.UserID != owner {
		return nil, nil, syncError(404, "NOT_FOUND")
	}
	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	classification, id := s.access.Classify(chat, connection)
	if classification == models.SourceIndependent {
		return chat, nil, nil
	}
	if classification != models.SourceRegisteredERP {
		return chat, nil, syncError(423, "DATA_SYNC_RECOVERING")
	}
	source, err := s.access.source(ctx, id)
	if err != nil {
		return nil, nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if err := s.access.binding(source, owner, chat, ConnectionFingerprint(connection)); err != nil {
		if errors.Is(err, ErrSourceForbidden) {
			return nil, nil, syncError(404, "NOT_FOUND")
		}
		return chat, nil, syncError(423, "DATA_SYNC_RECOVERING")
	}
	return chat, source, nil
}

func (s *DataSyncService) request(ctx context.Context, owner primitive.ObjectID, source, request string) (*models.DataSyncJob, error) {
	job, err := s.jobs.FindRequest(ctx, owner, source, request)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if job.Kind() != models.DataSyncOperation || job.SourceID != source || job.InitiatingOwnerID != owner || job.RequestID != request || job.Mode != "incremental" {
		return nil, syncError(409, "REQUEST_CONFLICT")
	}
	return job, nil
}

func (s *DataSyncService) Status(ctx context.Context, owner, chatID, request string) (*dtos.DataSyncStatusResponse, error) {
	if request != "" && !models.CanonicalSyncUUID(request) {
		return nil, syncError(400, "INVALID_REQUEST")
	}
	chat, source, err := s.authorize(ctx, owner, chatID)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(chat.DisplayName)
	if name == "" {
		name = chat.Connection.Database
	}
	status := &dtos.DataSyncStatusResponse{SchemaVersion: 1, DisplayName: name, BindingRevision: chat.SourceBindingRevision, Readiness: dtos.DataSyncReadiness{State: "not_applicable", Reasons: []string{}}, MaintenanceState: "idle", AskAllowed: true}
	if source == nil {
		if request != "" {
			status.RequestMatch = "not_found"
		}
		return status, nil
	}
	status.SourceID = source.SourceID
	status.SourceRevision = source.StateRevision
	status.MaintenanceState = string(source.Phase)
	status.AskAllowed = false
	if source.ActiveOperation != nil {
		if source.ActiveOperation.Kind == models.DataSyncOperation || source.ActiveOperation.Kind == models.ExternalMaintenanceOperation {
			status.OperationKind = source.ActiveOperation.Kind
		}
		if source.ActiveOperation.Kind == models.ExternalMaintenanceOperation {
			status.MaintenanceOperationRef = source.ActiveOperation.OperationID
		}
	}
	if request != "" {
		job, err := s.request(ctx, chat.UserID, source.SourceID, request)
		if err != nil {
			return nil, err
		}
		status.RequestMatch = "not_found"
		if job != nil {
			status.RequestMatch = "found"
			status.MatchedJob = dtos.NewDataSyncJobView(job, chat.UserID)
		}
	}
	if source.ActiveOperation != nil && source.ActiveOperation.JobID != "" {
		job, err := s.jobs.Get(ctx, source.ActiveOperation.JobID)
		if err != nil {
			return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
		}
		if job.SourceID != source.SourceID || !job.MatchesOperation(source.ActiveOperation) {
			return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
		}
		status.ActiveJob = dtos.NewDataSyncJobView(job, chat.UserID)
	}
	last, err := s.jobs.LatestSuccess(ctx, source.SourceID)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if last != nil {
		status.LatestSuccessfulSyncAt = last.FinishedAt
	}
	status.Readiness.State = "ready"
	if _, _, err := s.runner.Ready(source); err != nil {
		status.Readiness.State = "not_ready"
		status.Readiness.Reasons = append(status.Readiness.Reasons, "SYNC_RELEASE_UNAVAILABLE")
	}
	if !s.access.CanOperate(source, chat.UserID) {
		status.Readiness.Reasons = append(status.Readiness.Reasons, "SYNC_FORBIDDEN")
	}
	if err := s.access.ready(source); err != nil {
		status.BlockReason = "DATA_SYNC_RECOVERING"
		if errors.Is(err, ErrSourceSyncing) {
			status.BlockReason = "DATA_SYNC_IN_PROGRESS"
		}
	} else {
		lease, probeErr := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Shared, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
		if probeErr == nil {
			lease.Close()
			status.AskAllowed = true
		} else if errors.Is(probeErr, sourcegate.ErrBusy) {
			status.BlockReason = "DATA_SYNC_IN_PROGRESS"
		} else {
			return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
		}
	}
	status.CanStart = status.AskAllowed && status.Readiness.State == "ready" && s.access.CanOperate(source, chat.UserID)
	if status.CanStart {
		probe, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
		if err == nil {
			probe.Close()
		} else if errors.Is(err, sourcegate.ErrBusy) {
			status.CanStart = false
			status.BlockReason = "SOURCE_BUSY"
		} else {
			return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
		}
	}
	return status, nil
}

func (s *DataSyncService) GetJob(ctx context.Context, owner, chatID, jobID string) (*dtos.DataSyncJobView, error) {
	if !models.CanonicalSyncUUID(jobID) {
		return nil, syncError(404, "NOT_FOUND")
	}
	chat, source, err := s.authorize(ctx, owner, chatID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, syncError(404, "NOT_FOUND")
	}
	job, err := s.jobs.Get(ctx, jobID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, syncError(404, "NOT_FOUND")
	}
	if err != nil {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if job.Kind() != models.DataSyncOperation || job.SourceID != source.SourceID {
		return nil, syncError(404, "NOT_FOUND")
	}
	return dtos.NewDataSyncJobView(job, chat.UserID), nil
}

func freezeSyncJob(source *models.ERPSource, chat *models.Chat, request, instance, digest, bridge, aftersale string) (*models.DataSyncJob, error) {
	shops := append([]string(nil), source.ShopScope...)
	sort.Strings(shops)
	mapping := make(map[string]string, len(source.OrderSourceByShop))
	for shop, value := range source.OrderSourceByShop {
		mapping[shop] = value
	}
	job := &models.DataSyncJob{SchemaVersion: 1, OperationKind: models.ExternalMaintenanceOperation, JobID: uuid.NewString(), SourceID: source.SourceID, RequestID: request, RunnerVersion: models.DataSyncRunnerVersion, Mode: "incremental", ComponentManifestRef: source.ComponentManifestRef, ComponentDigest: digest, BridgeVersion: bridge, ConfigRevision: source.ConfigRevision, ExpectedShops: shops, SourceMapping: mapping, AftersaleSource: aftersale, InstanceID: instance, OperationID: uuid.NewString(), Status: models.DataSyncCreated, Stage: "preparing", CreatedAt: time.Now().UTC(), PartialCommits: "none_started"}
	if chat != nil {
		job.OperationKind = models.DataSyncOperation
		job.InitiatingOwnerID, job.InitiatingChatID = chat.UserID, chat.ID
		job.BindingRevision, job.ConnectionFingerprint = chat.SourceBindingRevision, chat.SourceConnectionFingerprint
	}
	if _, err := expectedSyncStates(job); err != nil {
		return nil, syncError(409, "SYNC_NOT_READY")
	}
	scope, _ := json.Marshal(struct {
		Shops     []string          `json:"shops"`
		Sources   map[string]string `json:"sources"`
		Aftersale string            `json:"aftersale"`
	}{shops, mapping, aftersale})
	job.ScopeDigest = syncDigest(scope)
	var fingerprint []byte
	if job.Kind() == models.ExternalMaintenanceOperation {
		fingerprint, _ = json.Marshal(struct {
			Source, Kind, Request, Mode, Component, Scope string
			Revision                                      int64
		}{job.SourceID, job.Kind(), request, job.Mode, job.ComponentManifestRef, job.ScopeDigest, job.ConfigRevision})
	} else {
		fingerprint, _ = json.Marshal(struct {
			Source, Owner, Request, Mode, Component, Scope string
			Revision                                       int64
		}{job.SourceID, job.InitiatingOwnerID.Hex(), request, job.Mode, job.ComponentManifestRef, job.ScopeDigest, job.ConfigRevision})
	}
	job.RequestFingerprint = syncDigest(fingerprint)
	return job, nil
}
func syncOperation(job *models.DataSyncJob, boot string) *models.ERPSourceOperation {
	return &models.ERPSourceOperation{OperationID: job.OperationID, Kind: job.Kind(), OwnerID: job.InitiatingOwnerID, ChatID: job.InitiatingChatID, RequestID: job.RequestID, JobID: job.JobID, ConfigRevision: job.ConfigRevision, ComponentManifestRef: job.ComponentManifestRef, InstanceID: job.InstanceID, BootID: boot}
}
func jobSyncIdentity(job *models.DataSyncJob) sourcegate.Identity {
	if job.Kind() == models.ExternalMaintenanceOperation {
		return sourcegate.Identity{SourceID: job.SourceID, OperationID: job.OperationID, ConfigRevision: job.ConfigRevision}
	}
	return sourcegate.Identity{SourceID: job.SourceID, OperationID: job.OperationID, OwnerID: job.InitiatingOwnerID.Hex(), ChatID: job.InitiatingChatID.Hex(), BindingRevision: job.BindingRevision, ConnectionFingerprint: job.ConnectionFingerprint, ConfigRevision: job.ConfigRevision}
}

func (s *DataSyncService) busy(ctx context.Context, source *models.ERPSource, owner primitive.ObjectID, request string) (*dtos.DataSyncJobView, error) {
	if job, err := s.request(ctx, owner, source.SourceID, request); job != nil || err != nil {
		return dtos.NewDataSyncJobView(job, owner), err
	}
	current, err := s.access.source(ctx, source.SourceID)
	if err != nil {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if current.ActiveOperation != nil {
		ref := ""
		if current.ActiveOperation.Kind == models.DataSyncOperation {
			ref = current.ActiveOperation.JobID
		}
		return nil, &DataSyncError{Status: 423, Code: "DATA_SYNC_IN_PROGRESS", SourceID: source.SourceID, JobRef: ref}
	}
	probe, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Shared, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
	if err == nil {
		probe.Close()
		return nil, syncError(409, "SOURCE_BUSY")
	}
	if errors.Is(err, sourcegate.ErrBusy) {
		return nil, syncError(423, "DATA_SYNC_RECOVERING")
	}
	return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
}

func (s *DataSyncService) CreateJob(ctx context.Context, owner, chatID, request string) (*dtos.DataSyncJobView, error) {
	if !models.CanonicalSyncUUID(request) {
		return nil, syncError(400, "INVALID_REQUEST")
	}
	if !s.begin() {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	defer s.work.Done()
	chat, source, err := s.authorize(ctx, owner, chatID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, syncError(404, "NOT_FOUND")
	}
	// Legal replay precedes new-start permission/readiness, including changed
	// configuration, empty operator lists, terminal jobs and late POST delivery.
	if old, err := s.request(ctx, chat.UserID, source.SourceID, request); old != nil || err != nil {
		return dtos.NewDataSyncJobView(old, chat.UserID), err
	}
	if !s.access.CanOperate(source, chat.UserID) {
		return nil, syncError(403, "SYNC_FORBIDDEN")
	}
	digest, bridge, err := s.runner.Ready(source)
	if err != nil {
		return nil, syncError(409, "SYNC_NOT_READY")
	}
	if err := s.access.ready(source); err != nil {
		code := "DATA_SYNC_RECOVERING"
		if errors.Is(err, ErrSourceSyncing) {
			code = "DATA_SYNC_IN_PROGRESS"
		}
		return nil, &DataSyncError{Status: 423, Code: code, SourceID: source.SourceID}
	}
	rt := s.access.syncRuntime.Sources[source.SourceID]
	job, err := freezeSyncJob(source, chat, request, s.access.instanceID, digest, bridge, rt.AftersaleSource)
	if err != nil {
		return nil, err
	}
	lease, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, jobSyncIdentity(job))
	if errors.Is(err, sourcegate.ErrBusy) {
		return s.busy(ctx, source, chat.UserID, request)
	}
	if err != nil {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	transferred := false
	defer func() {
		if !transferred {
			lease.Close()
		}
	}()
	freshChat, freshSource, err := s.authorize(ctx, owner, chatID)
	if err != nil {
		return nil, err
	}
	if old, err := s.request(ctx, chat.UserID, source.SourceID, request); old != nil || err != nil {
		return dtos.NewDataSyncJobView(old, chat.UserID), err
	}
	if freshSource == nil || freshSource.SourceID != source.SourceID || freshSource.ConfigRevision != source.ConfigRevision || !sameChatBinding(chat, freshChat) {
		return nil, syncError(409, "REQUEST_CONFLICT")
	}
	source = freshSource
	if !s.access.CanOperate(source, chat.UserID) {
		return nil, syncError(403, "SYNC_FORBIDDEN")
	}
	if s.access.ready(source) != nil {
		return nil, syncError(423, "DATA_SYNC_RECOVERING")
	}
	unreleased, err := s.jobs.ListUnreleased(ctx, source.SourceID)
	if err != nil {
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	if err := repositories.ConfirmSyncPredecessors(ctx, s.jobs, source, unreleased); err != nil {
		s.markRecovering(source)
		return nil, syncError(423, "DATA_SYNC_RECOVERING")
	}
	launch, err := s.runner.Prepare(ctx, source, job)
	if err != nil {
		return nil, syncError(409, "SYNC_NOT_READY")
	}
	if err := s.verifier.Observe(ctx, source, job, lease); err != nil {
		s.markRecovering(source)
		return nil, syncError(423, "DATA_SYNC_RECOVERING")
	}
	states, err := s.verifier.Preflight(ctx, source, job, lease)
	if err != nil {
		return nil, syncError(409, "SYNC_NOT_READY")
	}
	job.ExpectedStates = states
	boot, err := syncBootID()
	if err != nil {
		return nil, syncError(409, "SYNC_NOT_READY")
	}
	operation := syncOperation(job, boot)
	err = s.access.repo.UpdateStateCAS(ctx, source.SourceID, source.StateRevision, "", repositories.ERPSourceState{Phase: models.ERPSourceSyncing, ActiveOperation: operation, RecoveryVerifiedInstanceID: s.access.instanceID, LastReleasedOperationID: source.LastReleasedOperationID, FrozenJob: job})
	if err != nil {
		s.markRecovering(source)
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	// Read back both majority documents. An acknowledged call alone does not
	// authorize Start after another observer/callback has changed the fence.
	fenced, err := s.access.source(ctx, source.SourceID)
	if err != nil || fenced.ActiveOperation == nil || fenced.ActiveOperation.OperationID != job.OperationID || fenced.ConfigRevision != job.ConfigRevision {
		s.markRecovering(source)
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	source = fenced
	if err := s.jobs.Create(ctx, job); err != nil {
		s.markRecovering(source)
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	stored, err := s.jobs.Get(ctx, job.JobID)
	if err != nil || stored.RequestFingerprint != job.RequestFingerprint || stored.Status != models.DataSyncCreated || stored.Revision != 0 {
		s.markRecovering(source)
		return nil, syncError(503, "SYNC_STATE_UNAVAILABLE")
	}
	view := dtos.NewDataSyncJobView(stored, chat.UserID)
	s.work.Add(1)
	transferred = true
	go func() { defer s.work.Done(); s.execute(source, stored, lease, launch) }()
	return view, nil
}

func (s *DataSyncService) persist(previous, next *models.DataSyncJob) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.jobs.Advance(ctx, previous, next)
}
func (s *DataSyncService) markRecovering(source *models.ERPSource) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	current, err := s.access.source(ctx, source.SourceID)
	if err != nil {
		return
	}
	// Preserve a newer operation. A source clear whose publication failed can
	// re-enter recovering only for that exact last-released operation.
	if current.StateRevision != source.StateRevision && !(source.ActiveOperation != nil && current.ActiveOperation == nil && current.LastReleasedOperationID == source.ActiveOperation.OperationID) {
		return
	}
	op := ""
	if current.ActiveOperation != nil {
		op = current.ActiveOperation.OperationID
	}
	_ = s.access.repo.UpdateStateCAS(ctx, current.SourceID, current.StateRevision, op, repositories.ERPSourceState{Phase: models.ERPSourceRecovering, ActiveOperation: current.ActiveOperation, LastReleasedOperationID: current.LastReleasedOperationID, RecoveryVerifiedInstanceID: current.RecoveryVerifiedInstanceID})
}

// This polls cleanup evidence only after a known child exit/recovery stop.
// Admission still uses one nonblocking Observe and never queues a writer.
func (s *DataSyncService) awaitPGRelease(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease) error {
	if s.access.syncRuntime.Sources[source.SourceID].ControlReader.Reference == "" {
		return errERPSyncObservation
	}
	ctx, cancel := context.WithTimeout(ctx, syncDuration(s.access.syncRuntime.ControlSeconds, 15))
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.verifier.Observe(ctx, source, job, lease) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errERPSyncObservation
		case <-ticker.C:
		}
	}
}

func (s *DataSyncService) execute(source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease, launch *erpSyncLaunch) {
	defer lease.Close()
	result := s.runner.Run(s.ctx, source, job, lease, launch, func(next *models.DataSyncJob) error { return s.persist(job, next) })
	if !result.ExitConfirmed {
		s.markRecovering(source)
		return
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if s.awaitPGRelease(cleanup, source, job, lease) != nil {
		s.markRecovering(source)
		return
	}
	verification, verifyErr := s.verifier.Verify(cleanup, source, job, lease)
	if s.awaitPGRelease(cleanup, source, job, lease) != nil {
		s.markRecovering(source)
		return
	}
	next := *job
	next.Revision++
	next.Status = models.DataSyncRunning
	next.Stage = "verifying"
	next.ExitConfirmed = true
	next.ExitCode = result.ExitCode
	next.Verification = verification
	next.PendingOutcome = models.DataSyncFailed
	next.SafeErrorCode = result.SafeCode
	next.SafeSummary = "本次同步未全部完成，可能已有部分数据更新"
	next.PartialCommits = "unknown"
	for _, shop := range job.ShopResults {
		if shop.Status == "ok" && shop.RelatedOrdersRefetchCompleted {
			next.PartialCommits = "known_partial_commits"
			break
		}
	}
	if verifyErr != nil && next.SafeErrorCode == "" {
		next.SafeErrorCode = "SYNC_VERIFICATION_FAILED"
	}
	if result.SafeCode == "" && verifyErr == nil && verification != nil && verification.Passed {
		next.PendingOutcome = models.DataSyncSucceeded
		next.SafeErrorCode = ""
		next.SafeSummary = "本次增量同步已完成并通过核验"
		next.PartialCommits = "all_windows_confirmed"
	}
	if result.SafeCode == "RUNNER_INTERRUPTED" {
		next.PendingOutcome = models.DataSyncInterrupted
	}
	if s.persist(job, &next) != nil {
		s.markRecovering(source)
		return
	}
	*job = next
	if err := s.finish(cleanup, source, job, lease); err != nil {
		s.markRecovering(source)
	}
}

// releaseProof proves no surviving OFD reference after our last close. It is a
// nonblocking probe of the same source flock, never LOCK_UN or inode replacement.
func (s *DataSyncService) releaseProof(ctx context.Context, source *models.ERPSource, lease *sourcegate.Lease) error {
	if lease.JoinDescendants() != nil {
		return errSyncProcess
	}
	if lease.Close() != nil {
		return errSyncProcess
	}
	lease.Join()
	probe, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
	if err != nil {
		return errSyncProcess
	}
	return probe.Close()
}
func (s *DataSyncService) finish(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease) error {
	if s.releaseProof(ctx, source, lease) != nil {
		return errSyncProcess
	}
	if !job.Status.Terminal() {
		next := *job
		next.Revision++
		next.Status = job.PendingOutcome
		now := time.Now().UTC()
		next.FinishedAt = &now
		if s.persist(job, &next) != nil {
			return syncError(503, "SYNC_STATE_UNAVAILABLE")
		}
		*job = next
	}
	current, err := s.access.source(ctx, source.SourceID)
	if err != nil {
		return err
	}
	if current.ActiveOperation == nil || current.ActiveOperation.OperationID != job.OperationID {
		return errSyncProcess
	}
	if err := s.access.repo.UpdateStateCAS(ctx, current.SourceID, current.StateRevision, job.OperationID, repositories.ERPSourceState{Phase: models.ERPSourceIdle, LastReleasedOperationID: job.OperationID, RecoveryVerifiedInstanceID: s.access.instanceID}); err != nil {
		return err
	}
	confirmed, err := s.access.source(ctx, current.SourceID)
	if err != nil || confirmed.LastReleasedOperationID != job.OperationID {
		return errSyncProcess
	}
	if err := s.jobs.ConfirmRelease(ctx, job); err != nil {
		return err
	}
	job.ReleaseConfirmed = true
	return nil
}

func (s *DataSyncService) recoverSource(ctx context.Context, source *models.ERPSource) error {
	if source.RegistrationReceipt != nil && source.RegistrationReceipt.Phase == "view_pending" {
		return ErrSourceRecovering
	}
	jobs, err := s.jobs.ListUnreleased(ctx, source.SourceID)
	if err != nil {
		return err
	}
	pending := make([]models.DataSyncJob, 0, len(jobs))
	for i := range jobs {
		job := &jobs[i]
		if job.Status.Terminal() && source.LastReleasedOperationID == job.OperationID {
			if s.jobs.ConfirmRelease(ctx, job) != nil {
				return errSyncProcess
			}
		} else {
			pending = append(pending, *job)
		}
	}
	if len(pending) > 1 {
		s.markRecovering(source)
		return errSyncProcess
	}
	var job *models.DataSyncJob
	if len(pending) == 1 {
		job = &pending[0]
	}
	if source.ActiveOperation != nil {
		if source.ActiveOperation.Kind != models.DataSyncOperation && source.ActiveOperation.Kind != models.ExternalMaintenanceOperation {
			s.markRecovering(source)
			return errSyncProcess
		}
		if job != nil && !job.MatchesOperation(source.ActiveOperation) {
			s.markRecovering(source)
			return errSyncProcess
		}
		if job == nil {
			store, ok := s.access.repo.(repositories.SyncFenceStore)
			if !ok {
				return errSyncProcess
			}
			frozen, err := store.FrozenSyncJob(ctx, source.SourceID, source.ActiveOperation.OperationID)
			if err != nil {
				return err
			}
			if err := s.jobs.Create(ctx, frozen); err != nil && !mongo.IsDuplicateKeyError(err) {
				return err
			}
			job, err = s.jobs.Get(ctx, frozen.JobID)
			if err != nil {
				return err
			}
			if !job.MatchesOperation(source.ActiveOperation) {
				return errSyncProcess
			}
		}
	}
	op := uuid.NewString()
	identity := sourcegate.Identity{SourceID: source.SourceID, OperationID: op}
	if job != nil {
		op = job.OperationID
		identity = jobSyncIdentity(job)
	}
	lease, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, identity)
	if errors.Is(err, sourcegate.ErrBusy) && job != nil && job.Process != nil {
		s.markRecovering(source)
		observed, readErr := s.access.source(ctx, source.SourceID)
		if readErr != nil {
			return readErr
		}
		if s.runner.Recover(ctx, observed, job, nil) != nil {
			return errSyncProcess
		}
		source = observed
		lease, err = s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, identity)
	}
	if err != nil {
		s.markRecovering(source)
		return err
	}
	defer lease.Close()
	current, err := s.access.source(ctx, source.SourceID)
	if err != nil {
		return err
	}
	if current.StateRevision != source.StateRevision {
		return errSyncProcess
	}
	for _, binding := range current.ChatBindings {
		if !binding.Confirmed {
			s.markRecovering(current)
			return errSyncProcess
		}
	}
	active := current.ActiveOperation
	if job != nil && active == nil {
		boot, _ := syncBootID()
		active = syncOperation(job, boot)
	}
	oldOperation := ""
	if current.ActiveOperation != nil {
		oldOperation = current.ActiveOperation.OperationID
	}
	if err := s.access.repo.UpdateStateCAS(ctx, current.SourceID, current.StateRevision, oldOperation, repositories.ERPSourceState{Phase: models.ERPSourceRecovering, ActiveOperation: active, LastReleasedOperationID: current.LastReleasedOperationID, RecoveryVerifiedInstanceID: s.access.instanceID}); err != nil {
		return err
	}
	current, err = s.access.source(ctx, current.SourceID)
	if err != nil {
		return err
	}
	if s.runner.Recover(ctx, current, job, lease) != nil || s.awaitPGRelease(ctx, current, job, lease) != nil {
		return errSyncProcess
	}
	if job == nil {
		if s.releaseProof(ctx, current, lease) != nil {
			return errSyncProcess
		}
		return s.access.repo.UpdateStateCAS(ctx, current.SourceID, current.StateRevision, "", repositories.ERPSourceState{Phase: models.ERPSourceIdle, LastReleasedOperationID: current.LastReleasedOperationID, RecoveryVerifiedInstanceID: s.access.instanceID})
	}
	if !job.Status.Terminal() {
		next := *job
		next.Revision++
		next.Status = models.DataSyncRunning
		next.Stage = "verifying"
		next.PendingOutcome = models.DataSyncInterrupted
		next.ExitConfirmed = true
		next.SafeErrorCode = "RUNNER_INTERRUPTED"
		next.SafeSummary = "同步因进程中断结束，未自动重跑，可能已有部分数据更新"
		next.PartialCommits = "unknown"
		if s.persist(job, &next) != nil {
			return errSyncProcess
		}
		*job = next
	}
	return s.finish(ctx, current, job, lease)
}
