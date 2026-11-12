package services

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	goredis "github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/embedding"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/mongodb"
	redisrepo "yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/sourcegate"
	"yanfachatbi/pkg/vectordb"
)

// This adapter exists only in this _test.go. It launches an actual controlled
// SQL-writing child, not an official ERP bridge and never a production fallback.
type dataSyncTestRunner struct {
	ERPSyncRunner
	pg               *erpSyncPGFixture
	scenario, record string
	starts           atomic.Int32
	survivorDB       *sql.DB
	survivor         *sql.Conn
}

func (r *dataSyncTestRunner) Load(*models.ERPSource) error { return nil }
func (r *dataSyncTestRunner) Ready(*models.ERPSource) (string, string, error) {
	return strings.Repeat("b", 64), "test-probe/1", nil
}
func (r *dataSyncTestRunner) Prepare(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob) (*erpSyncLaunch, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	digest, err := syncHashFile(file)
	file.Close()
	if err != nil {
		return nil, err
	}
	return &erpSyncLaunch{executable: executable, args: []string{"-test.run=^TestDataSyncControlledWriterChild$"}, environment: []string{"YANFA_SQL_CHILD=1", "YANFA_SQL_CHILD_DSN=" + r.pg.writerDSN, "YANFA_SQL_CHILD_CASE=" + r.scenario, "YANFA_SQL_CHILD_RECORD=" + r.record}, executableDigest: digest}, nil
}
func (r *dataSyncTestRunner) Run(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease, launch *erpSyncLaunch, persist func(*models.DataSyncJob) error) erpSyncRunResult {
	r.starts.Add(1)
	result := r.ERPSyncRunner.Run(ctx, source, job, lease, launch, persist)
	if r.scenario == "surviving_pg" && result.ExitConfirmed {
		db, err := sql.Open("postgres", r.pg.writerDSN)
		if err != nil {
			return erpSyncRunResult{SafeCode: "RUNNER_FAILED"}
		}
		db.SetMaxIdleConns(0)
		conn, err := db.Conn(context.Background())
		if err != nil {
			db.Close()
			return erpSyncRunResult{SafeCode: "RUNNER_FAILED"}
		}
		var locked bool
		if conn.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock(7319041)`).Scan(&locked) != nil || !locked {
			conn.Close()
			db.Close()
			return erpSyncRunResult{SafeCode: "RUNNER_FAILED"}
		}
		r.survivorDB = db
		r.survivor = conn
	}
	return result
}

type dataSyncWorld struct {
	f       *sourceAccessFixture
	pg      *erpSyncPGFixture
	chat    *models.Chat
	source  *models.ERPSource
	jobs    repositories.DataSyncRepository
	runner  *dataSyncTestRunner
	service *DataSyncService
}

func newDataSyncWorld(t *testing.T, scenario string) *dataSyncWorld {
	t.Helper()
	f := newSourceAccessFixture(t, 0)
	// Startup now opens the real same-UID listener. Use the approved gate path,
	// rather than relaxing its trusted-parent checks for a /tmp fixture.
	directory := fmt.Sprintf("/run/user/%d/yanfa-bi-chat/source-gates", os.Getuid())
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal("manager must provision the isolated 0700 gate directory")
	}
	t.Cleanup(func() { gate.Close() })
	f.gate, f.service.gate, f.service.lockDirectory = gate, gate, directory
	port, reader := "19000", "isolated_reader"
	connection := models.Connection{Type: "postgresql", Host: "127.0.0.1", Port: &port, Username: &reader, Database: "bi_agent"}
	registered, err := f.service.RegisterApproved(context.Background(), models.ERPSource{EnvironmentRef: "isolated-only", PhysicalDatabase: "bi_agent", ApprovedEndpointAliases: []string{EndpointFingerprint(connection)}, ApprovedConnectionFingerprints: []string{ConnectionFingerprint(connection)}, ReaderRoleRefs: []string{"isolated-reader-ref"}, WriterRoleRef: "isolated-writer-ref", ComponentManifestRef: "isolated-component", ApprovalRefs: map[string]string{"Q15-02": "isolated-only-evidence"}, ViewAccountIDs: []primitive.ObjectID{f.owner}})
	if err != nil {
		t.Fatal(err)
	}
	if f.repo.UpdateStateCAS(context.Background(), registered.SourceID, 0, "", repositories.ERPSourceState{Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: f.service.instanceID}) != nil {
		t.Fatal("fixture initial recovery failed")
	}
	f.sources, f.connections = []*models.ERPSource{registered}, []models.Connection{connection}
	f.service.associations[EndpointFingerprint(connection)] = registered.SourceID
	chat := f.bindChat(t, 0)
	pg := newERPSyncPGFixture(t)
	source, err := f.repo.Get(context.Background(), f.sources[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	source.ConfigRevision++
	source.ComponentManifestRef = "test-only-probe"
	source.ShopScope = []string{"1", "2", "3", "4", "5", "6", "7"}
	source.OrderSourceByShop = pg.mapping
	source.SyncOperatorAccountIDs = []primitive.ObjectID{f.owner}
	if err := f.repo.UpdateConfigCAS(context.Background(), source.SourceID, source.ConfigRevision-1, source); err != nil {
		t.Fatal(err)
	}
	f.service.syncRuntime = config.ERPSyncRuntime{ControlSeconds: 1, Sources: map[string]config.ERPSourceRuntime{source.SourceID: pg.runtime}}
	jobs := repositories.NewDataSyncRepository(f.database)
	if jobs.EnsureIndexes(context.Background()) != nil {
		t.Fatal("job indexes failed")
	}
	runner := &dataSyncTestRunner{ERPSyncRunner: NewERPSyncRunner(config.ERPSyncRuntime{RunSeconds: 10, TermSeconds: 1, KillSeconds: 1}, uint32(os.Getuid())), pg: pg, scenario: scenario, record: filepath.Join(t.TempDir(), "writer-start.jsonl")}
	service := NewDataSyncService(f.service, jobs, runner, NewERPSyncVerifier(f.service.syncRuntime, f.service.instanceID))
	w := &dataSyncWorld{f: f, pg: pg, chat: chat, source: source, jobs: jobs, runner: runner, service: service}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		service.Shutdown(ctx)
		if runner.survivor != nil {
			runner.survivor.Close()
		}
		if runner.survivorDB != nil {
			runner.survivorDB.Close()
		}
	})
	return w
}

type dataSyncFaultStore struct {
	repositories.DataSyncRepository
	disconnected                    repositories.DataSyncRepository
	failCreate, failTerminal        bool
	identityStored, identityRelease chan struct{}
	once                            sync.Once
}

func (r *dataSyncFaultStore) Create(ctx context.Context, job *models.DataSyncJob) error {
	if r.failCreate {
		return r.disconnected.Create(ctx, job)
	}
	return r.DataSyncRepository.Create(ctx, job)
}
func (r *dataSyncFaultStore) Advance(ctx context.Context, old, next *models.DataSyncJob) error {
	if r.failTerminal && next.Status.Terminal() {
		return r.disconnected.Advance(ctx, old, next)
	}
	if err := r.DataSyncRepository.Advance(ctx, old, next); err != nil {
		return err
	}
	if r.identityStored != nil && old.Process == nil && next.Process != nil {
		r.once.Do(func() { close(r.identityStored); <-r.identityRelease })
	}
	return nil
}
func dataSyncDisconnected(t *testing.T, w *dataSyncWorld) repositories.DataSyncRepository {
	t.Helper()
	client, err := mongo.Connect(context.Background(), options.Client().ApplyURI(os.Getenv("ASSISTANT_TEST_MONGO_URI")))
	if err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewDataSyncRepository(client.Database(w.f.database.Name()))
	if err := client.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestDataSyncActualWriterDurableStartReplayAndLastRelease(t *testing.T) {
	w := newDataSyncWorld(t, "ok")
	barrier := &dataSyncFaultStore{DataSyncRepository: w.jobs, identityStored: make(chan struct{}), identityRelease: make(chan struct{})}
	w.service.jobs = barrier
	request := uuid.NewString()
	httpContext, cancel := context.WithCancel(context.Background())
	view, err := w.service.CreateJob(httpContext, w.f.owner.Hex(), w.chat.ID.Hex(), request)
	if err != nil {
		t.Fatal(err)
	}
	cancel() // HTTP is no longer the child lifetime.
	select {
	case <-barrier.identityStored:
	case <-time.After(15 * time.Second):
		t.Fatal("real process identity was not persisted")
	}
	stored, err := w.jobs.Get(context.Background(), view.JobID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := w.f.repo.Get(context.Background(), w.source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Process == nil || source.ActiveOperation == nil || source.ActiveOperation.JobID != stored.JobID || source.ActiveOperation.OperationID != stored.OperationID {
		t.Fatal("Start preceded durable fence/job/process identity")
	}
	if _, err := os.Stat(w.runner.record); !os.IsNotExist(err) {
		t.Fatal("child crossed start-frame before process identity persistence returned")
	}
	requireERPSyncGate(t, w.f.gate, source, true)
	var work sync.WaitGroup
	for range 8 {
		work.Add(1)
		go func() {
			defer work.Done()
			replay, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
			if err != nil || replay.JobID != view.JobID {
				t.Errorf("concurrent same-ID replay: %v", err)
			}
		}()
	}
	work.Wait()
	close(barrier.identityRelease)
	w.service.work.Wait()
	final, err := w.jobs.Get(context.Background(), view.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != models.DataSyncSucceeded || !final.ReleaseConfirmed || final.Verification == nil || !final.Verification.Passed || len(final.Verification.Rows) != 14 {
		t.Fatalf("actual child/PG/storage release failed: status=%s code=%s", final.Status, final.SafeErrorCode)
	}
	source, err = w.f.repo.Get(context.Background(), source.SourceID)
	if err != nil || source.Phase != models.ERPSourceIdle || source.ActiveOperation != nil || source.LastReleasedOperationID != final.OperationID {
		t.Fatal("published success without source clear")
	}
	requireERPSyncGate(t, w.f.gate, source, false)
	lines := strings.Split(strings.TrimSpace(string(waitERPSyncFile(t, w.runner.record))), "\n")
	if len(lines) != 1 || w.runner.starts.Load() != 1 {
		t.Fatal("same operation started multiple real children")
	}
	source.ConfigRevision++
	source.SyncOperatorAccountIDs = nil
	if w.f.repo.UpdateConfigCAS(context.Background(), source.SourceID, source.ConfigRevision-1, source) != nil {
		t.Fatal("permission fixture change failed")
	}
	late, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
	if err != nil || late.JobID != final.JobID || late.Revision != final.Revision || late.Status != models.DataSyncSucceeded {
		t.Fatal("terminal/late POST re-ran or lost original operation")
	}
	status, err := w.service.Status(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
	if err != nil || status.RequestMatch != "found" || status.LatestSuccessfulSyncAt == nil || !status.LatestSuccessfulSyncAt.Equal(*final.FinishedAt) {
		t.Fatal("lost-202 recovery or last-success publication failed")
	}
}

func TestDataSyncStorageFailuresRestartNeverRuns(t *testing.T) {
	for _, fault := range []string{"create", "terminal"} {
		t.Run(fault, func(t *testing.T) {
			w := newDataSyncWorld(t, "ok")
			faults := &dataSyncFaultStore{DataSyncRepository: w.jobs, disconnected: dataSyncDisconnected(t, w), failCreate: fault == "create", failTerminal: fault == "terminal"}
			w.service.jobs = faults
			request := uuid.NewString()
			_, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
			if fault == "create" && err == nil {
				t.Fatal("disconnected durable job store accepted execution")
			}
			if fault == "terminal" && err != nil {
				t.Fatal(err)
			}
			w.service.work.Wait()
			source, err := w.f.repo.Get(context.Background(), w.source.SourceID)
			if err != nil || source.ActiveOperation == nil || source.Phase != models.ERPSourceRecovering {
				t.Fatal("storage uncertainty removed recovery veto")
			}
			if fault == "create" {
				if w.runner.starts.Load() != 0 {
					t.Fatal("child started before confirmed job")
				}
				if _, err := os.Stat(w.runner.record); !os.IsNotExist(err) {
					t.Fatal("real child received frame before durable job")
				}
			}
			if _, err := w.jobs.LatestSuccess(context.Background(), source.SourceID); !errors.Is(err, mongo.ErrNoDocuments) {
				t.Fatal("storage failure updated last-success")
			}
			if w.service.Shutdown(context.Background()) != nil {
				t.Fatal("original service did not stop")
			}
			restartedAccess := *w.f.service
			restartedAccess.instanceID += ":new-lifetime"
			restarted := NewDataSyncService(&restartedAccess, w.jobs, w.runner, NewERPSyncVerifier(restartedAccess.syncRuntime, restartedAccess.instanceID))
			defer restarted.Shutdown(context.Background())
			before := w.runner.starts.Load()
			if restarted.Startup(context.Background()) != nil {
				t.Fatal("recovery startup failed")
			}
			job, err := w.jobs.FindRequest(context.Background(), w.f.owner, source.SourceID, request)
			if err != nil || job.Status != models.DataSyncInterrupted || !job.ReleaseConfirmed {
				t.Fatal("restart failed to reconstruct interrupted without rerunning")
			}
			if w.runner.starts.Load() != before {
				t.Fatal("startup automatically re-ran old increment")
			}
			if _, err := w.jobs.LatestSuccess(context.Background(), source.SourceID); !errors.Is(err, mongo.ErrNoDocuments) {
				t.Fatal("full watermark/pending success was promoted after crash")
			}
		})
	}
}

func TestDataSyncLivePGSessionKeepsRecoveryVeto(t *testing.T) {
	w := newDataSyncWorld(t, "surviving_pg")
	request := uuid.NewString()
	view, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
	if err != nil {
		t.Fatal(err)
	}
	w.service.work.Wait()
	source, err := w.f.repo.Get(context.Background(), w.source.SourceID)
	if err != nil || source.Phase != models.ERPSourceRecovering || source.ActiveOperation == nil {
		t.Fatal("Wait/exit released source while real PG session survived")
	}
	status, err := w.service.Status(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
	if err != nil || status.AskAllowed || status.CanStart {
		t.Fatal("recovering PG writer allowed execution")
	}
	if w.service.recoverSource(context.Background(), source) == nil {
		t.Fatal("recovery ignored actual PG writer session")
	}
	w.runner.survivor.Close()
	w.runner.survivorDB.Close()
	w.runner.survivor = nil
	w.runner.survivorDB = nil
	source, err = w.f.repo.Get(context.Background(), source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if w.service.recoverSource(context.Background(), source) != nil {
		t.Fatal("confirmed PG cleanup could not finish recovery")
	}
	job, err := w.jobs.Get(context.Background(), view.JobID)
	if err != nil || job.Status != models.DataSyncInterrupted || !job.ReleaseConfirmed || w.runner.starts.Load() != 1 {
		t.Fatal("recovery re-ran/promoted old execution")
	}
}

func TestDataSyncOfficialReleaseAbsentRefusesLaunch(t *testing.T) {
	w := newDataSyncWorld(t, "ok")
	w.service.runner = NewERPSyncRunner(w.f.service.syncRuntime, w.f.service.uid)
	if _, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), uuid.NewString()); err == nil {
		t.Fatal("test probe or current package substituted for official release")
	}
	current, err := w.f.repo.Get(context.Background(), w.source.SourceID)
	if err != nil || current.ActiveOperation != nil || current.Phase != models.ERPSourceIdle || w.runner.starts.Load() != 0 {
		t.Fatal("missing release mutated source or launched process")
	}
}

func TestDataSyncActualWriterRejectsPartialAndLag(t *testing.T) {
	for _, scenario := range []string{"lag", "shop_error_exit0", "missing_tail", "sql_empty_error"} {
		t.Run(scenario, func(t *testing.T) {
			w := newDataSyncWorld(t, scenario)
			view, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			w.service.work.Wait()
			job, err := w.jobs.Get(context.Background(), view.JobID)
			if err != nil || job.Status != models.DataSyncFailed || !job.ReleaseConfirmed {
				t.Fatal("actual partial/lag/NULL-error batch became success or lost release")
			}
			if job.ExitCode == nil || *job.ExitCode != 0 {
				t.Fatal("fixture must prove independent rejection of actual exit0")
			}
			if _, err := w.jobs.LatestSuccess(context.Background(), job.SourceID); !errors.Is(err, mongo.ErrNoDocuments) {
				t.Fatal("failed verification updated last-success")
			}
			if w.runner.starts.Load() != 1 {
				t.Fatal("failure automatically retried")
			}
		})
	}
}

// The helper owns the actual PG connection/advisory lock and synthetic writes.
// Its contract is explicitly test-only; no ERP requests or financial assertions.
func TestDataSyncControlledWriterChild(t *testing.T) {
	if os.Getenv("YANFA_SQL_CHILD") != "1" {
		return
	}
	os.Exit(dataSyncControlledWriterChild())
}
func dataSyncControlledWriterChild() int {
	var frame syncStartFrame
	input := bufio.NewReader(os.Stdin)
	raw, err := input.ReadBytes('\n')
	if err != nil || len(raw) > syncLineLimit || strictSyncJSON(raw, &frame) != nil || frame.Protocol != erpBridgeProtocol || frame.Type != "start" {
		return 79
	}
	lock := os.NewFile(3, "test-inherited-lock")
	if _, err := lock.Stat(); err != nil {
		return 79
	}
	defer lock.Close()
	if file, err := os.OpenFile(os.Getenv("YANFA_SQL_CHILD_RECORD"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		fmt.Fprintln(file, frame.OperationID)
		file.Close()
	} else {
		return 79
	}
	db, err := sql.Open("postgres", os.Getenv("YANFA_SQL_CHILD_DSN"))
	if err != nil {
		return 80
	}
	db.SetMaxIdleConns(0)
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		return 80
	}
	defer conn.Close()
	var locked bool
	if conn.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock(7319041)`).Scan(&locked) != nil || !locked {
		return 80
	}
	cutoff := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Truncate(time.Second)
	scenario := os.Getenv("YANFA_SQL_CHILD_CASE")
	shops := []string{"1", "2", "3", "4", "5", "6", "7"}
	seq := int64(1)
	header := func(kind string) syncEventHeader {
		value := syncEventHeader{Protocol: erpBridgeProtocol, JobID: frame.JobID, SourceID: frame.SourceID, OperationID: frame.OperationID, Seq: seq, Type: kind}
		seq++
		return value
	}
	emit := func(value interface{}) bool {
		raw, err := json.Marshal(value)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintln(os.Stdout, string(raw))
		return err == nil
	}
	if !emit(syncStartedEvent{syncEventHeader: header("run_started"), BridgeVersion: "test-probe/1", ComponentRef: "test-only-probe", Mode: "incremental", Cutoff: cutoff.Format(time.RFC3339), ConfigRevision: frame.ConfigRevision, ScopeDigest: frame.ScopeDigest, ExpectedShopIDs: shops, ExpectedStates: frame.ExpectedStates}) {
		return 81
	}
	results := make([]models.ERPSyncShopResult, 0, 7)
	for _, shop := range shops {
		tx, err := conn.BeginTx(context.Background(), nil)
		if err != nil {
			return 82
		}
		order := ""
		for _, state := range frame.ExpectedStates {
			if state.ShopID != shop {
				continue
			}
			if state.Entity == "orders" {
				order = state.Source
			}
			if scenario == "lag" && shop == "7" && state.Entity == "aftersales_occurrence" {
				continue
			}
			if _, err := tx.Exec(`UPDATE bi.sync_state SET watermark=$4,last_error_code=NULL,last_success_at=$4 WHERE source=$1 AND entity=$2 AND shop_id=$3`, state.Source, state.Entity, state.ShopID, cutoff); err != nil {
				tx.Rollback()
				return 82
			}
			if scenario == "sql_empty_error" && shop == "1" && state.Entity == "orders" {
				if _, err := tx.Exec(`UPDATE bi.sync_state SET last_error_code='' WHERE source=$1 AND entity=$2 AND shop_id=$3`, state.Source, state.Entity, state.ShopID); err != nil {
					tx.Rollback()
					return 82
				}
			}
		}
		if tx.Commit() != nil {
			return 82
		}
		result := models.ERPSyncShopResult{ShopID: shop, Status: "ok", OrderSource: order, RelatedOrdersRefetchCompleted: true}
		if scenario == "shop_error_exit0" && shop == "1" {
			result.Status = "failed"
			result.RelatedOrdersRefetchCompleted = false
			result.SafeErrorCode = "SHOP_FAILED"
		}
		results = append(results, result)
		if !emit(syncShopEvent{syncEventHeader: header("shop_finished"), ERPSyncShopResult: result}) {
			return 81
		}
	}
	if scenario == "missing_tail" {
		return 0
	}
	ok := 0
	for _, result := range results {
		if result.Status == "ok" {
			ok++
		}
	}
	if !emit(syncFinishedEvent{syncEventHeader: header("run_finished"), Cutoff: cutoff.Format(time.RFC3339), ExpectedCount: 7, ProcessedCount: 7, SucceededCount: ok, FailedCount: 7 - ok, ShopResultsDigest: syncShopDigest(results)}) {
		return 81
	}
	return 0
}

type dataSyncR1ReleaseObservation struct {
	source *models.ERPSource
	job    *models.DataSyncJob
	err    error
}

type dataSyncR1BindingObservation struct {
	before, after *models.ERPSource
	job           *models.DataSyncJob
	ownEX         bool
	err           error
}

// Every write delegates to the production Mongo repository. Only the return
// from the first real sync source-clear CAS is delayed; no witness is invented.
type dataSyncR1SourceBarrier struct {
	repositories.ERPSourceRepository
	repositories.SyncFenceStore
	jobs           repositories.DataSyncRepository
	owner          primitive.ObjectID
	request        string
	cleared        chan dataSyncR1ReleaseObservation
	release        chan struct{}
	releaseOnce    sync.Once
	mu             sync.Mutex
	operation      string
	clearOnce      sync.Once
	bindingContext context.Context
	bindings       []dataSyncR1BindingObservation
}

// ChangeBinding carries its lease through the real Chat store context; its
// source-state repository calls intentionally receive the caller context.
type dataSyncR1BindingStore struct {
	BindingChatStore
	barrier *dataSyncR1SourceBarrier
}

func (s *dataSyncR1BindingStore) ReadCurrent(ctx context.Context, owner, chat primitive.ObjectID) (*models.Chat, error) {
	s.barrier.mu.Lock()
	s.barrier.bindingContext = ctx
	s.barrier.mu.Unlock()
	return s.BindingChatStore.ReadCurrent(ctx, owner, chat)
}

func (r *dataSyncR1SourceBarrier) unblock() { r.releaseOnce.Do(func() { close(r.release) }) }

func (r *dataSyncR1SourceBarrier) UpdateStateCAS(ctx context.Context, id string, revision int64, operation string, next repositories.ERPSourceState) error {
	if next.ActiveOperation != nil && next.ActiveOperation.Kind == "sync" && next.ActiveOperation.RequestID == r.request {
		r.mu.Lock()
		r.operation = next.ActiveOperation.OperationID
		r.mu.Unlock()
	}
	binding := next.ActiveOperation != nil && next.ActiveOperation.Kind == "binding_change"
	var observed dataSyncR1BindingObservation
	if binding {
		observed.before, observed.err = r.ERPSourceRepository.Get(ctx, id)
		r.mu.Lock()
		bindingContext := r.bindingContext
		r.mu.Unlock()
		if bindingContext != nil {
			identity, err := sourcegate.ContextIdentity(bindingContext, id)
			if err == nil {
				lease, err := sourcegate.FromContext(bindingContext, identity)
				observed.ownEX = err == nil && lease.Mode() == sourcegate.Exclusive && identity.OperationID == next.ActiveOperation.OperationID
			}
		}
	}
	err := r.ERPSourceRepository.UpdateStateCAS(ctx, id, revision, operation, next)
	if binding && err == nil {
		observed.after, err = r.ERPSourceRepository.Get(ctx, id)
		if err != nil {
			observed.err = err
		}
		job, readErr := r.jobs.FindRequest(ctx, next.ActiveOperation.OwnerID, id, r.request)
		observed.job = job
		if readErr != nil {
			observed.err = readErr
		}
		r.mu.Lock()
		r.bindings = append(r.bindings, observed)
		r.mu.Unlock()
		// Observation failures belong to the test, not the delegated CAS result.
		return nil
	}
	if err != nil {
		return err
	}
	r.mu.Lock()
	target := r.operation
	r.mu.Unlock()
	if target != "" && operation == target && next.Phase == models.ERPSourceIdle && next.ActiveOperation == nil && next.LastReleasedOperationID == target {
		r.clearOnce.Do(func() {
			source, readErr := r.ERPSourceRepository.Get(ctx, id)
			var job *models.DataSyncJob
			if readErr == nil {
				job, readErr = r.jobs.FindRequest(ctx, r.owner, id, r.request)
			}
			r.cleared <- dataSyncR1ReleaseObservation{source: source, job: job, err: readErr}
			select {
			case <-r.release:
			case <-ctx.Done():
			}
		})
	}
	return nil
}

// This validator belongs only to the fixture's unique Mongo database. All
// production repository instances see the same actual write rejection while
// primary reads and non-publication updates remain healthy.
func dataSyncR1RejectPublicationWrites(t *testing.T, w *dataSyncWorld) func() {
	t.Helper()
	var once sync.Once
	restore := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := w.f.database.RunCommand(ctx, bson.D{{Key: "collMod", Value: "data_sync_jobs"}, {Key: "validator", Value: bson.M{}}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}, {Key: "writeConcern", Value: bson.M{"w": "majority"}}}).Err(); err != nil {
				t.Error("could not restore the isolated publication validator")
			}
		})
	}
	t.Cleanup(restore)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.f.database.RunCommand(ctx, bson.D{{Key: "collMod", Value: "data_sync_jobs"}, {Key: "validator", Value: bson.M{"release_confirmed": bson.M{"$ne": true}}}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}, {Key: "writeConcern", Value: bson.M{"w": "majority"}}}).Err(); err != nil {
		t.Fatal("manager fixture requires collMod permission in its isolated test database")
	}
	return restore
}

func dataSyncR1PublicationWriteRejected(err error) bool {
	var server mongo.ServerError
	return errors.As(err, &server) && server.HasErrorCode(121)
}

type dataSyncR1BindingEffects struct {
	sourceAccessEffects
	readerDSN string
	control   *sql.DB
	reads     atomic.Int32
}

func (e *dataSyncR1BindingEffects) Probe(ctx context.Context, connection models.Connection) error {
	if err := e.sourceAccessEffects.Probe(ctx, connection); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", e.readerDSN)
	if err != nil {
		return err
	}
	db.SetMaxIdleConns(0)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var database string
	var pid int
	if err := tx.QueryRowContext(ctx, "SELECT current_database(), pg_backend_pid()").Scan(&database, &pid); err != nil {
		return err
	}
	if database != "bi_agent" {
		return errors.New("isolated binding probe targeted another database")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := conn.Close(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var gone bool
		if err := e.control.QueryRowContext(ctx, "SELECT NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1)", pid).Scan(&gone); err != nil {
			return err
		}
		if gone {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	e.reads.Add(1)
	return nil
}

func dataSyncR1ApprovedBindingTarget(t *testing.T, w *dataSyncWorld) models.Connection {
	t.Helper()
	parsed, err := url.Parse(w.pg.readerDSN)
	if err != nil {
		t.Fatal("isolated reader URL invalid")
	}
	connection := w.chat.Connection.Clone()
	port, username := parsed.Port(), parsed.User.Username()
	if port == "" {
		port = "5432"
	}
	password, _ := parsed.User.Password()
	connection.Host, connection.Port = parsed.Hostname(), &port
	connection.Username, connection.Password = &username, &password
	connection.Database = strings.TrimPrefix(parsed.Path, "/")
	source, err := w.f.repo.Get(context.Background(), w.source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, fingerprint := EndpointFingerprint(connection), ConnectionFingerprint(connection)
	endpointKnown, connectionKnown := false, false
	for _, value := range source.ApprovedEndpointAliases {
		endpointKnown = endpointKnown || value == endpoint
	}
	for _, value := range source.ApprovedConnectionFingerprints {
		connectionKnown = connectionKnown || value == fingerprint
	}
	if !endpointKnown {
		source.ApprovedEndpointAliases = append(source.ApprovedEndpointAliases, endpoint)
	}
	if !connectionKnown {
		source.ApprovedConnectionFingerprints = append(source.ApprovedConnectionFingerprints, fingerprint)
	}
	source.ConfigRevision++
	if err := w.f.repo.UpdateConfigCAS(context.Background(), source.SourceID, source.ConfigRevision-1, source); err != nil {
		t.Fatal(err)
	}
	w.f.service.associations[endpoint] = source.SourceID
	w.source = source
	return connection
}

func TestDataSyncReleasePublicationBindingInterleaving(t *testing.T) {
	for _, scenario := range []string{"publication_pending", "publication_write_failure"} {
		t.Run(scenario, func(t *testing.T) {
			w := newDataSyncWorld(t, "ok")
			target := dataSyncR1ApprovedBindingTarget(t, w)
			request := uuid.NewString()
			barrier := &dataSyncR1SourceBarrier{ERPSourceRepository: w.f.repo, SyncFenceStore: w.f.repo.(repositories.SyncFenceStore), jobs: w.jobs, owner: w.f.owner, request: request, cleared: make(chan dataSyncR1ReleaseObservation, 1), release: make(chan struct{})}
			// Registered before any launch: Fatal, cancellation and assertion failures
			// cannot strand the actual source-clear callback or shutdown WaitGroup.
			t.Cleanup(barrier.unblock)
			defer barrier.unblock()
			w.f.service.repo = barrier
			var restorePublication func()
			if scenario == "publication_write_failure" {
				restorePublication = dataSyncR1RejectPublicationWrites(t, w)
			}
			view, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
			if err != nil {
				t.Fatal(err)
			}
			var cleared dataSyncR1ReleaseObservation
			select {
			case cleared = <-barrier.cleared:
			case <-time.After(15 * time.Second):
				t.Fatal("real sync source-clear CAS was not observed")
			}
			if cleared.err != nil || cleared.source == nil || cleared.job == nil {
				t.Fatal("could not read confirmed source-clear/job evidence")
			}
			job := cleared.job
			if job.JobID != view.JobID || job.Status != models.DataSyncSucceeded || job.ReleaseConfirmed || job.FinishedAt == nil || job.Verification == nil || !job.Verification.Passed || len(job.Verification.Rows) != 14 || !job.ExitConfirmed {
				t.Fatal("barrier was not after actual child/14-key/terminal completion and before publication")
			}
			if cleared.source.Phase != models.ERPSourceIdle || cleared.source.ActiveOperation != nil || cleared.source.LastReleasedOperationID != job.OperationID {
				t.Fatal("barrier did not expose the real sync release witness")
			}
			requireERPSyncGate(t, w.f.gate, cleared.source, false)
			if _, err := w.jobs.LatestSuccess(context.Background(), job.SourceID); !errors.Is(err, mongo.ErrNoDocuments) {
				t.Fatal("unpublished terminal was counted as last-success")
			}
			if scenario == "publication_write_failure" {
				attempt := *job
				if err := w.jobs.ConfirmRelease(context.Background(), &attempt); !dataSyncR1PublicationWriteRejected(err) {
					t.Fatal("production ConfirmRelease did not reach the actual Mongo publication write rejection")
				}
				stored, err := w.jobs.Get(context.Background(), job.JobID)
				if err != nil || stored.ReleaseConfirmed || stored.Revision != job.Revision {
					t.Fatal("failed publication invented a durable update")
				}
			}
			previous, err := w.f.chats.ReadCurrent(context.Background(), w.f.owner, w.chat.ID)
			if err != nil {
				t.Fatal(err)
			}
			next := *previous
			next.Connection = target.Clone()
			effects := &dataSyncR1BindingEffects{sourceAccessEffects: sourceAccessEffects{fixture: w.f, chatID: w.chat.ID}, readerDSN: w.pg.readerDSN, control: w.pg.db}
			bindingCtx, cancelBinding := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancelBinding()
			change, bindingErr := w.f.service.ChangeBinding(bindingCtx, w.f.owner, previous, &next, target, &dataSyncR1BindingStore{BindingChatStore: w.f.chats, barrier: barrier}, effects)
			if bindingErr == nil {
				if change == nil {
					t.Fatal("real ChangeBinding returned no completion")
				}
				select {
				case <-change.done:
					bindingErr = change.Wait()
				case <-bindingCtx.Done():
					t.Fatal("real binding terminal was not reached")
				}
				if bindingErr != nil || effects.reads.Load() != 1 {
					t.Fatal("real admitted binding did not finish its reader probe and persistence")
				}
			} else if !errors.Is(bindingErr, ErrSourceRecovering) && !errors.Is(bindingErr, ErrSourceSyncing) && !errors.Is(bindingErr, ErrSourceStateUnavailable) && !dataSyncR1PublicationWriteRejected(bindingErr) && !errors.Is(bindingErr, repositories.ErrDataSyncConflict) {
				t.Fatalf("legitimate same-source binding failed outside publication admission: %v", bindingErr)
			}
			after, err := w.f.repo.Get(context.Background(), job.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			chatAfter, err := w.f.chats.ReadCurrent(context.Background(), w.f.owner, w.chat.ID)
			if err != nil {
				t.Fatal(err)
			}
			published, err := w.jobs.Get(context.Background(), job.JobID)
			if err != nil {
				t.Fatal(err)
			}
			barrier.mu.Lock()
			writes := append([]dataSyncR1BindingObservation(nil), barrier.bindings...)
			barrier.mu.Unlock()
			for _, write := range writes {
				if write.err != nil || write.before == nil || write.after == nil || write.job == nil || !write.ownEX {
					t.Fatal("binding evidence did not come from the real primary repositories under its own EX")
				}
				if !write.job.ReleaseConfirmed {
					t.Errorf("binding mutated before predecessor publication: before=%q after=%q job_release_confirmed=%t", write.before.LastReleasedOperationID, write.after.LastReleasedOperationID, write.job.ReleaseConfirmed)
				}
			}
			if scenario == "publication_write_failure" {
				if bindingErr == nil || len(writes) != 0 || !sameChatBinding(previous, chatAfter) || after.LastReleasedOperationID != job.OperationID || after.ActiveOperation != nil || published.ReleaseConfirmed || published.Revision != job.Revision {
					t.Errorf("publication write failure allowed binding mutation: binding_error=%v binding_writes=%d witness=%q job_release_confirmed=%t binding_revision=%d->%d", bindingErr, len(writes), after.LastReleasedOperationID, published.ReleaseConfirmed, previous.SourceBindingRevision, chatAfter.SourceBindingRevision)
				}
			} else if bindingErr != nil && (!sameChatBinding(previous, chatAfter) || len(writes) != 0 || after.LastReleasedOperationID != job.OperationID || after.ActiveOperation != nil) {
				t.Error("publication admission refused binding after consuming its predecessor witness or changing Chat")
			} else if bindingErr == nil && !published.ReleaseConfirmed {
				t.Errorf("binding completed while original terminal stayed unpublished: witness=%q original_operation=%q status=%s release_confirmed=%t", after.LastReleasedOperationID, job.OperationID, published.Status, published.ReleaseConfirmed)
			}
			barrier.unblock()
			w.service.work.Wait()
			if scenario == "publication_write_failure" {
				failed, err := w.jobs.Get(context.Background(), job.JobID)
				current, sourceErr := w.f.repo.Get(context.Background(), job.SourceID)
				if err != nil || sourceErr != nil {
					t.Fatal("healthy primary client became unreadable during publication fault")
				}
				if failed.ReleaseConfirmed || failed.Status != models.DataSyncSucceeded || current.LastReleasedOperationID != job.OperationID {
					t.Error("failed publication lost its durable terminal/witness or was falsely published")
				}
				if _, err := w.jobs.LatestSuccess(context.Background(), job.SourceID); !errors.Is(err, mongo.ErrNoDocuments) {
					t.Error("failed publication updated last-success")
				}
				restorePublication()
				if err := w.service.recoverSource(context.Background(), current); err != nil {
					t.Fatal("real recovery could not finish after publication storage returned")
				}
			}
			final, err := w.jobs.Get(context.Background(), job.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if final.Status != models.DataSyncSucceeded || !final.ReleaseConfirmed || final.FinishedAt == nil || !final.FinishedAt.Equal(*job.FinishedAt) {
				t.Errorf("completed predecessor never published after real binding interleaving: status=%s release_confirmed=%t", final.Status, final.ReleaseConfirmed)
			}
			last, lastErr := w.jobs.LatestSuccess(context.Background(), job.SourceID)
			if lastErr != nil || last.JobID != job.JobID || last.FinishedAt == nil || !last.FinishedAt.Equal(*job.FinishedAt) {
				t.Error("binding interleaving removed the completed sync from last-success")
			}
			original, err := w.service.GetJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), job.JobID)
			if err != nil || original.Status != models.DataSyncSucceeded || !original.ReleaseConfirmed || original.FinishedAt == nil {
				t.Error("original request remained hidden as recovering after successful release")
			}
			if w.runner.starts.Load() != 1 || len(strings.Split(strings.TrimSpace(string(waitERPSyncFile(t, w.runner.record))), "\n")) != 1 {
				t.Fatal("binding/publication handling re-ran the original child")
			}
			replay, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
			if err != nil || replay.JobID != job.JobID || replay.Status != models.DataSyncSucceeded || !replay.ReleaseConfirmed || w.runner.starts.Load() != 1 {
				t.Error("old request replay did not retain the released terminal without another child")
			}
			if bindingErr != nil && final.ReleaseConfirmed {
				// A safe refusal while publication was pending must still permit
				// the same real binding after its predecessor actually publishes.
				currentChat, err := w.f.chats.ReadCurrent(context.Background(), w.f.owner, w.chat.ID)
				if err != nil {
					t.Fatal(err)
				}
				retry := *currentChat
				retry.Connection = target.Clone()
				retryEffects := &dataSyncR1BindingEffects{sourceAccessEffects: sourceAccessEffects{fixture: w.f, chatID: w.chat.ID}, readerDSN: w.pg.readerDSN, control: w.pg.db}
				retryCtx, cancelRetry := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancelRetry()
				change, err := w.f.service.ChangeBinding(retryCtx, w.f.owner, currentChat, &retry, target, w.f.chats, retryEffects)
				if err != nil || change == nil {
					t.Fatal("published predecessor still prevented the legal binding")
				}
				select {
				case <-change.done:
					if change.Wait() != nil || retryEffects.reads.Load() != 1 {
						t.Fatal("post-publication binding did not finish its real reader/persistence path")
					}
				case <-retryCtx.Done():
					t.Fatal("post-publication binding did not reach its terminal")
				}
			}
			successor, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), uuid.NewString())
			if err != nil {
				t.Errorf("new explicit sync blocked by completed predecessor after ChangeBinding: %v", err)
				return
			}
			w.service.work.Wait()
			completed, err := w.jobs.Get(context.Background(), successor.JobID)
			if err != nil || completed.Status != models.DataSyncSucceeded || !completed.ReleaseConfirmed || completed.Verification == nil || !completed.Verification.Passed || len(completed.Verification.Rows) != 14 || w.runner.starts.Load() != 2 {
				t.Fatal("legal new request did not complete through actual writer/14-key/release path")
			}
			current, err := w.f.repo.Get(context.Background(), job.SourceID)
			if err != nil || current.Phase != models.ERPSourceIdle || current.ActiveOperation != nil {
				t.Fatal("successor did not release its source")
			}
			requireERPSyncGate(t, w.f.gate, current, false)
		})
	}
}

// The Python child only supplies protocol/process behavior. After it exits, this
// fixture's actual synthetic writer advances the frozen PG rows under the same
// EX, so a lagging database cannot conceal acceptance of an illegal wire frame.
type dataSyncR1CaseAliasRunner struct {
	*dataSyncTestRunner
	t            *testing.T
	protocolCase string
	setupErrors  chan error
	fixtureRows  atomic.Int32
}

func (r *dataSyncR1CaseAliasRunner) Prepare(context.Context, *models.ERPSource, *models.DataSyncJob) (*erpSyncLaunch, error) {
	return erpSyncProbeLaunch(r.t, r.protocolCase, r.record), nil
}

func (r *dataSyncR1CaseAliasRunner) Run(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease, launch *erpSyncLaunch, persist func(*models.DataSyncJob) error) erpSyncRunResult {
	r.starts.Add(1)
	result := r.ERPSyncRunner.Run(ctx, source, job, lease, launch, persist)
	if !result.ExitConfirmed || job.Cutoff == nil {
		return result
	}
	err := r.advanceFixture(ctx, job, lease)
	if err != nil {
		select {
		case r.setupErrors <- err:
		default:
		}
	}
	return result
}

func (r *dataSyncR1CaseAliasRunner) advanceFixture(ctx context.Context, job *models.DataSyncJob, lease *sourcegate.Lease) error {
	if lease == nil || lease.Mode() != sourcegate.Exclusive || lease.Check(jobSyncIdentity(job)) != nil {
		return errors.New("case-alias fixture lost source EX")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", r.pg.writerDSN)
	if err != nil {
		return err
	}
	db.SetMaxIdleConns(0)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(7319041)").Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("case-alias fixture could not obtain the actual writer lock")
	}
	for _, state := range job.ExpectedStates {
		result, err := conn.ExecContext(ctx, `UPDATE bi.sync_state SET watermark=$4,last_error_code=NULL,last_success_at=$4 WHERE source=$1 AND entity=$2 AND shop_id=$3`, state.Source, state.Entity, state.ShopID, *job.Cutoff)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return errors.New("case-alias fixture did not update one exact frozen key")
		}
		r.fixtureRows.Add(1)
	}
	return nil
}

func TestDataSyncCaseAliasProtocolNeverPublishes(t *testing.T) {
	for _, scenario := range []string{"case_alias_count_conflict", "case_alias_required_only", "case_alias_nested_state", "case_alias_seq_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			w := newDataSyncWorld(t, "ok")
			runner := &dataSyncR1CaseAliasRunner{dataSyncTestRunner: w.runner, t: t, protocolCase: scenario, setupErrors: make(chan error, 1)}
			w.service.runner = runner
			request := uuid.NewString()
			view, err := w.service.CreateJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
			if err != nil {
				t.Fatal(err)
			}
			w.service.work.Wait()
			select {
			case <-runner.setupErrors:
				t.Fatal("actual synthetic writer setup failed before protocol publication assertions")
			default:
			}
			job, err := w.jobs.Get(context.Background(), view.JobID)
			if err != nil {
				t.Fatal(err)
			}
			verified := job.Verification != nil && job.Verification.Passed
			t.Logf("actual case-alias result: scenario=%s status=%s safe_code=%q run_finished=%t fixture_keys=%d verified=%t release_confirmed=%t", scenario, job.Status, job.SafeErrorCode, job.RunFinished, runner.fixtureRows.Load(), verified, job.ReleaseConfirmed)
			if job.Status != models.DataSyncFailed || job.SafeErrorCode != "RUNNER_PROTOCOL_INVALID" || !job.ReleaseConfirmed {
				t.Errorf("illegal case-alias frame reached durable success/publication: status=%s code=%q release_confirmed=%t", job.Status, job.SafeErrorCode, job.ReleaseConfirmed)
			}
			if !job.ExitConfirmed || job.Process == nil || !job.Process.StartFrameConfirmed {
				t.Fatal("case-alias fault did not pass through actual child/start-frame/exit management")
			}
			if job.Cutoff != nil && (runner.fixtureRows.Load() != 14 || job.Verification == nil || len(job.Verification.Rows) != 14 || !job.Verification.Passed) {
				t.Fatal("real 14-key verifier evidence was absent or lag masked the protocol case")
			}
			if _, err := w.jobs.LatestSuccess(context.Background(), job.SourceID); !errors.Is(err, mongo.ErrNoDocuments) {
				t.Error("case-alias output updated latest-success")
			}
			api, err := w.service.GetJob(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), job.JobID)
			if err != nil || api.Status != models.DataSyncFailed || api.SafeErrorCode != "RUNNER_PROTOCOL_INVALID" || !api.ReleaseConfirmed {
				t.Error("safe job consumer exposed success for a non-closed-schema event")
			}
			status, err := w.service.Status(context.Background(), w.f.owner.Hex(), w.chat.ID.Hex(), request)
			if err != nil || status.LatestSuccessfulSyncAt != nil || status.RequestMatch != "found" || status.MatchedJob == nil || status.MatchedJob.Status != models.DataSyncFailed {
				t.Error("status/request replay treated an illegal event as a successful sync")
			}
			var raw bson.Raw
			if err := w.f.database.Collection("data_sync_jobs").FindOne(context.Background(), bson.M{"job_id": job.JobID}).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			for _, wireOnly := range []string{"ORDERS_COUNT", "BRIDGE_VERSION", "BEFORE_WATERMARK", "2099-01-01T00:00:00+08:00"} {
				if bytes.Contains(raw, []byte(wireOnly)) {
					t.Error("raw case-alias stdout reached the actual Mongo job")
				}
			}
			source, err := w.f.repo.Get(context.Background(), job.SourceID)
			if err != nil || source.Phase != models.ERPSourceIdle || source.ActiveOperation != nil || source.LastReleasedOperationID != job.OperationID {
				t.Fatal("actual protocol failure lost final source cleanup")
			}
			requireERPSyncGate(t, w.f.gate, source, false)
			lines := strings.Split(strings.TrimSpace(string(waitERPSyncFile(t, runner.record))), "\n")
			if len(lines) != 1 || runner.starts.Load() != 1 {
				t.Fatal("case-alias failure started or retried more than one actual child")
			}
		})
	}
}

// Pause only after a real primary snapshot has been read. Every repository
// operation still delegates to Mongo; the concurrent metadata writer uses the
// same production Chat service without inheriting the paused request's snapshot.
type dataSyncR2CallerChatStore struct {
	repositories.ChatRepository
	snapshot *sourceAccessTerminal
	armed    atomic.Bool
}

func (r *dataSyncR2CallerChatStore) FindByID(id primitive.ObjectID) (*models.Chat, error) {
	chat, err := r.ChatRepository.FindByID(id)
	if err == nil && r.armed.CompareAndSwap(true, false) {
		r.snapshot.enter()
	}
	return chat, err
}

func dataSyncR2AssertRejectedChat(t *testing.T, expected, actual *models.Chat) {
	t.Helper()
	if !sameChatBinding(expected, actual) || !reflect.DeepEqual(expected.Connection, actual.Connection) {
		t.Error("rejected real Chat Update changed its connection or binding")
	}
	if actual.SelectedCollections != expected.SelectedCollections {
		t.Errorf("rejected real Chat Update changed selected_collections: before=%q after=%q", expected.SelectedCollections, actual.SelectedCollections)
	}
	if actual.DisplayName != expected.DisplayName {
		t.Errorf("rejected real Chat Update changed display_name: expected=%q after=%q", expected.DisplayName, actual.DisplayName)
	}
	if !reflect.DeepEqual(actual.PreferredLLMModel, expected.PreferredLLMModel) || actual.Settings != expected.Settings {
		t.Error("rejected real Chat Update changed preferred model/settings or rolled back concurrent metadata")
	}
}

func TestDataSyncChatUpdatePublicationFailurePreservesSelection(t *testing.T) {
	if os.Getenv("SOURCE_ACCESS_TEST_REDIS_ADDR") == "" || os.Getenv("SOURCE_ACCESS_TEST_QDRANT_ADDR") == "" {
		t.Skip("UNVERIFIED: real Chat Update descendants require isolated Redis and Qdrant inputs")
	}
	for _, scenario := range []string{"rejected_connection", "concurrent_metadata"} {
		t.Run(scenario, func(t *testing.T) {
			w := newDataSyncWorld(t, "ok")
			ctx := context.Background()
			oldKey, oldDataKey := config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey
			config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey = "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"
			t.Cleanup(func() {
				config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey = oldKey, oldDataKey
			})
			target := dataSyncR1ApprovedBindingTarget(t, w)
			oldTable, nextTable := "t39_caller_old_"+w.chat.ID.Hex(), "t39_caller_new_"+w.chat.ID.Hex()
			for _, table := range []string{oldTable, nextTable} {
				if _, err := w.pg.db.ExecContext(ctx, `CREATE TABLE public."`+table+`" (id integer PRIMARY KEY, caller_value text)`); err != nil {
					t.Fatal("isolated caller table setup failed")
				}
				table := table
				t.Cleanup(func() { w.pg.db.ExecContext(context.Background(), `DROP TABLE IF EXISTS public."`+table+`"`) })
				if _, err := w.pg.db.ExecContext(ctx, `INSERT INTO public."`+table+`" VALUES (1, 'isolated caller record')`); err != nil {
					t.Fatal("isolated caller row setup failed")
				}
			}
			if _, err := w.pg.db.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO `+pq.QuoteIdentifier(*target.Username)+`; GRANT SELECT ON public."`+oldTable+`", public."`+nextTable+`" TO `+pq.QuoteIdentifier(*target.Username)); err != nil {
				t.Fatal("isolated caller reader grant failed")
			}
			encrypted := w.chat.Connection.Clone()
			if err := utils.EncryptConnection(&encrypted); err != nil {
				t.Fatal("synthetic caller connection encryption failed")
			}
			if _, err := w.f.database.Collection("chats").UpdateOne(ctx, bson.M{"_id": w.chat.ID, "user_id": w.f.owner}, bson.M{"$set": bson.M{"connection": encrypted}}); err != nil {
				t.Fatal(err)
			}
			initialName, initialModel := "original caller name", "original-caller-model"
			if err := w.f.chats.PatchMetadata(ctx, w.f.owner, w.chat.ID, repositories.ChatMetadataPatch{DisplayName: &initialName, SelectedCollections: &oldTable, PreferredLLMModel: &initialModel}); err != nil {
				t.Fatal(err)
			}
			redisClient := goredis.NewClient(&goredis.Options{Addr: os.Getenv("SOURCE_ACCESS_TEST_REDIS_ADDR")})
			t.Cleanup(func() { redisClient.Close() })
			if err := redisClient.Ping(ctx).Err(); err != nil {
				t.Fatal("isolated caller Redis unavailable")
			}
			redisStore := redisrepo.NewRedisRepositories(redisClient)
			manager, err := dbmanager.NewManager(redisStore, config.Env.SchemaEncryptionKey)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			modelBarrier, embeddingBarrier := newSourceAccessTerminal(), newSourceAccessTerminal()
			var modelCalls, embeddingCalls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(request.URL.Path, "/embeddings") {
					embeddingCalls.Add(1)
					embeddingBarrier.enter()
					var input struct {
						Input      []string `json:"input"`
						Dimensions int      `json:"dimensions"`
					}
					if json.NewDecoder(request.Body).Decode(&input) != nil {
						http.Error(response, "invalid isolated embedding input", http.StatusBadRequest)
						return
					}
					if input.Dimensions == 0 {
						input.Dimensions = 1536
					}
					data := make([]map[string]interface{}, len(input.Input))
					for i := range data {
						vector := make([]float32, input.Dimensions)
						vector[0] = 1
						data[i] = map[string]interface{}{"object": "embedding", "index": i, "embedding": vector}
					}
					json.NewEncoder(response).Encode(map[string]interface{}{"object": "list", "data": data, "model": "text-embedding-3-small"})
					return
				}
				modelCalls.Add(1)
				modelBarrier.enter()
				content, _ := json.Marshal(map[string]interface{}{"tables": []map[string]interface{}{{"table_name": nextTable, "description": "caller retry selected table", "field_descriptions": []map[string]string{{"field_name": "caller_value", "description": "caller retry field note"}}}}})
				json.NewEncoder(response).Encode(map[string]interface{}{"id": "isolated-caller-kb", "object": "chat.completion", "choices": []map[string]interface{}{{"index": 0, "message": map[string]string{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}}})
			}))
			t.Cleanup(endpoint.Close)
			// Release blocked HTTP replies even if a later setup prerequisite fails.
			t.Cleanup(func() { modelBarrier.finish(); embeddingBarrier.finish() })
			model, err := llm.NewOpenAIClient(llm.Config{APIKey: "isolated-only", BaseURL: endpoint.URL + "/v1", Model: "isolated-caller-kb"})
			if err != nil {
				t.Fatal(err)
			}
			embeddings, err := embedding.NewOpenAIProvider("isolated-only", "text-embedding-3-small", endpoint.URL+"/v1")
			if err != nil {
				t.Fatal(err)
			}
			vectorHost, vectorPort, err := net.SplitHostPort(os.Getenv("SOURCE_ACCESS_TEST_QDRANT_ADDR"))
			if err != nil {
				t.Fatal("isolated caller Qdrant address invalid")
			}
			vectors, err := vectordb.NewQdrantClient(vectordb.QdrantConfig{Host: vectorHost, Port: vectorPort})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { vectors.Close() })
			vectorization := NewVectorizationService(embeddings, vectors)
			if err := vectorization.EnsureReady(ctx); err != nil {
				t.Fatal("isolated caller vector collections unavailable")
			}
			kb := repositories.NewKnowledgeBaseRepository(&mongodb.MongoDBClient{Client: w.f.client, Config: mongodb.MongoDbConfigModel{DatabaseName: w.f.database.Name()}})
			snapshot := newSourceAccessTerminal()
			chatStore := &dataSyncR2CallerChatStore{ChatRepository: w.f.chats, snapshot: snapshot}
			w.f.chats, w.f.service.chats = chatStore, chatStore
			caller := NewChatService(chatStore, manager, model, nil, redisStore, nil, vectorization, kb, nil, nil, w.f.service).(*chatService)
			manager.SetStreamHandler(caller)
			requestID := uuid.NewString()
			barrier := &dataSyncR1SourceBarrier{ERPSourceRepository: w.f.repo, SyncFenceStore: w.f.repo.(repositories.SyncFenceStore), jobs: w.jobs, owner: w.f.owner, request: requestID, cleared: make(chan dataSyncR1ReleaseObservation, 1), release: make(chan struct{})}
			w.f.service.repo = barrier
			// This cleanup runs before SDK/manager/database cleanup. It also joins
			// the real retained binding references on Fatal paths after retry.
			var failedCallerDone <-chan struct{}
			t.Cleanup(func() {
				snapshot.finish()
				barrier.unblock()
				modelBarrier.finish()
				embeddingBarrier.finish()
				cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if failedCallerDone != nil {
					select {
					case <-failedCallerDone:
					case <-cleanup.Done():
						t.Error("real rejected caller did not return during cleanup")
						return
					}
				}
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					lease, err := w.f.gate.Acquire(cleanup, sourceTarget(w.source), sourcegate.Exclusive, sourcegate.Identity{SourceID: w.source.SourceID, OperationID: uuid.NewString()})
					if err == nil {
						lease.Close()
						break
					}
					if !errors.Is(err, sourcegate.ErrBusy) {
						t.Error("caller descendant cleanup could not observe its source lock")
						return
					}
					select {
					case <-cleanup.Done():
						t.Error("caller descendants retained EX after real terminal replies")
						return
					case <-ticker.C:
					}
				}
				if err := vectorization.DeleteChatVectors(cleanup, w.chat.ID.Hex()); err != nil {
					t.Error("could not remove isolated caller vectors")
				}
			})
			defer barrier.unblock()
			defer snapshot.finish()
			defer modelBarrier.finish()
			defer embeddingBarrier.finish()
			restorePublication := dataSyncR1RejectPublicationWrites(t, w)
			view, err := w.service.CreateJob(ctx, w.f.owner.Hex(), w.chat.ID.Hex(), requestID)
			if err != nil {
				t.Fatal("real predecessor sync was not accepted")
			}
			var cleared dataSyncR1ReleaseObservation
			select {
			case cleared = <-barrier.cleared:
			case <-time.After(15 * time.Second):
				t.Fatal("real predecessor did not reach its source-clear barrier")
			}
			if cleared.err != nil || cleared.source == nil || cleared.job == nil {
				t.Fatal("real source-clear evidence unavailable")
			}
			job := cleared.job
			if job.JobID != view.JobID || job.Status != models.DataSyncSucceeded || job.ReleaseConfirmed || job.FinishedAt == nil || !job.ExitConfirmed || job.Verification == nil || !job.Verification.Passed || len(job.Verification.Rows) != 14 || cleared.source.Phase != models.ERPSourceIdle || cleared.source.ActiveOperation != nil || cleared.source.LastReleasedOperationID != job.OperationID {
				t.Fatal("caller fixture did not observe actual child/14-key/terminal/source clear before publication")
			}
			requireERPSyncGate(t, w.f.gate, cleared.source, false)
			attempt := *job
			if err := w.jobs.ConfirmRelease(ctx, &attempt); !dataSyncR1PublicationWriteRejected(err) {
				t.Fatal("only-publication Mongo121 fault did not reach the real repository")
			}
			before, err := w.f.chats.ReadCurrent(ctx, w.f.owner, w.chat.ID)
			if err != nil || before.SelectedCollections != oldTable || before.DisplayName != initialName || before.PreferredLLMModel == nil || *before.PreferredLLMModel != initialModel {
				t.Fatal("primary caller selection/metadata prerequisite was not persisted")
			}
			requestedName, requestedModel, requestedNonTech := "connection request rename", "connection-request-model", true
			connectionRequest := dtos.CreateConnectionRequest{Type: target.Type, Host: target.Host, Port: target.Port, Username: *target.Username, Password: target.Password, Database: target.Database, AuthDatabase: target.AuthDatabase, UseSSL: target.UseSSL, SSLMode: target.SSLMode, SSLCertURL: target.SSLCertURL, SSLKeyURL: target.SSLKeyURL, SSLRootCertURL: target.SSLRootCertURL}
			// SelectedCollections is deliberately omitted in this real caller.
			update := &dtos.UpdateChatRequest{Connection: &connectionRequest, DisplayName: &requestedName, PreferredLLMModel: &requestedModel, Settings: &dtos.CreateChatSettings{NonTechMode: &requestedNonTech}}
			type updateResult struct {
				response *dtos.ChatResponse
				status   uint32
				err      error
			}
			returned, callerDone := make(chan updateResult, 1), make(chan struct{})
			failedCallerDone = callerDone
			chatStore.armed.Store(scenario == "concurrent_metadata")
			go func() {
				defer close(callerDone)
				response, status, err := caller.Update(w.f.owner.Hex(), w.chat.ID.Hex(), update)
				returned <- updateResult{response, status, err}
			}()
			expected := before
			if scenario == "concurrent_metadata" {
				waitSourceAccess(t, snapshot.started)
				name, preferred, autoViz := "concurrent accepted rename", "concurrent-accepted-model", true
				metadata, status, err := caller.Update(w.f.owner.Hex(), w.chat.ID.Hex(), &dtos.UpdateChatRequest{DisplayName: &name, PreferredLLMModel: &preferred, Settings: &dtos.CreateChatSettings{AutoGenerateVisualization: &autoViz}})
				if err != nil || status != http.StatusOK || metadata == nil || metadata.DisplayName != name || !metadata.Settings.AutoGenerateVisualization {
					t.Fatal("real concurrent metadata-only Update was blocked by publication failure")
				}
				expected, err = w.f.chats.ReadCurrent(ctx, w.f.owner, w.chat.ID)
				if err != nil || expected.SelectedCollections != oldTable || expected.DisplayName != name || expected.PreferredLLMModel == nil || *expected.PreferredLLMModel != preferred || !expected.Settings.AutoGenerateVisualization {
					t.Fatal("concurrent metadata did not reach primary before connection rejection")
				}
				snapshot.finish()
			}
			var rejected updateResult
			select {
			case rejected = <-returned:
			case <-time.After(15 * time.Second):
				t.Fatal("real Chat Update did not return its publication refusal")
			}
			if rejected.response != nil || rejected.status != http.StatusConflict || !errors.Is(rejected.err, ErrSourceRecovering) {
				t.Fatalf("real Chat Update did not reach predecessor publication admission: status=%d error=%v", rejected.status, rejected.err)
			}
			after, err := w.f.chats.ReadCurrent(ctx, w.f.owner, w.chat.ID)
			if err != nil {
				t.Fatal("healthy primary Chat was unreadable after refusal")
			}
			t.Logf("real refused Update: scenario=%s status=%d selected_before=%q selected_after=%q expected_name=%q actual_name=%q", scenario, rejected.status, expected.SelectedCollections, after.SelectedCollections, expected.DisplayName, after.DisplayName)
			dataSyncR2AssertRejectedChat(t, expected, after)
			sourceAfter, sourceErr := w.f.repo.Get(ctx, job.SourceID)
			jobAfter, jobErr := w.jobs.Get(ctx, job.JobID)
			if sourceErr != nil || jobErr != nil {
				t.Fatal("healthy primary source/job was unreadable during publication rejection")
			}
			if !reflect.DeepEqual(cleared.source, sourceAfter) || !reflect.DeepEqual(job, jobAfter) {
				t.Error("rejected outer caller changed original witness/active/source bindings or unpublished job")
			}
			if w.runner.starts.Load() != 1 || len(strings.Split(strings.TrimSpace(string(waitERPSyncFile(t, w.runner.record))), "\n")) != 1 || modelCalls.Load() != 0 || embeddingCalls.Load() != 0 {
				t.Fatal("publication refusal started a second writer or schema descendants")
			}
			if _, exists := manager.GetConnectionInfo(w.chat.ID.Hex()); exists {
				t.Error("rejected caller reached the production connection manager")
			}
			requireERPSyncGate(t, w.f.gate, sourceAfter, false)
			barrier.unblock()
			w.service.work.Wait()
			current, err := w.f.repo.Get(ctx, job.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			restorePublication()
			if err := w.service.recoverSource(ctx, current); err != nil {
				t.Fatal("real predecessor publication could not recover after storage returned")
			}
			replay, err := w.service.CreateJob(ctx, w.f.owner.Hex(), w.chat.ID.Hex(), requestID)
			if err != nil || replay.JobID != job.JobID || replay.Status != models.DataSyncSucceeded || !replay.ReleaseConfirmed || w.runner.starts.Load() != 1 {
				t.Fatal("original request was not replayed without another actual child")
			}
			// The admitted retry explicitly selects a different real table. Schema,
			// Mongo KB and vectors must consume this new selection under retained EX.
			retry := *update
			retry.SelectedCollections = &nextTable
			response, status, err := caller.Update(w.f.owner.Hex(), w.chat.ID.Hex(), &retry)
			if err != nil || status != http.StatusOK || response == nil || response.SelectedCollections != nextTable || response.DisplayName != requestedName {
				t.Fatalf("published predecessor still rejected the real connection Update retry: status=%d error=%v", status, err)
			}
			for _, terminal := range []*sourceAccessTerminal{modelBarrier, embeddingBarrier} {
				waitSourceAccess(t, terminal.started)
				active, err := w.f.repo.Get(ctx, job.SourceID)
				if err != nil || active.Phase != models.ERPSourceRecovering || active.ActiveOperation == nil || active.ActiveOperation.Kind != "binding_change" {
					t.Fatal("real schema descendant was not inside its binding recovery fence")
				}
				requireERPSyncGate(t, w.f.gate, active, true)
				selected, err := w.f.chats.ReadCurrent(ctx, w.f.owner, w.chat.ID)
				if err != nil || selected.SelectedCollections != nextTable || selected.Connection.CurrentSchema == nil || !strings.Contains(*selected.Connection.CurrentSchema, nextTable) || !strings.Contains(*selected.Connection.CurrentSchema, "caller_value") || strings.Contains(*selected.Connection.CurrentSchema, oldTable) {
					t.Fatal("protected real schema descendants used the old selection or selection was saved too late")
				}
				terminal.finish()
			}
			waitSourceAccessIdle(t, w.f)
			final, err := w.f.chats.ReadCurrent(ctx, w.f.owner, w.chat.ID)
			if err != nil {
				t.Fatal(err)
			}
			plaintext := final.Connection.Clone()
			utils.DecryptConnection(&plaintext)
			if final.SourceID != before.SourceID || !final.SourceBindingConfirmed || final.SourceBindingRevision != before.SourceBindingRevision+1 || final.ConnectionRevision != before.ConnectionRevision+1 || ConnectionFingerprint(plaintext) != ConnectionFingerprint(target) || final.SelectedCollections != nextTable || final.DisplayName != requestedName || final.PreferredLLMModel == nil || *final.PreferredLLMModel != requestedModel || !final.Settings.NonTechMode || final.Settings.AutoGenerateVisualization != expected.Settings.AutoGenerateVisualization {
				t.Fatal("real successful retry did not persist the new selection/metadata/connection/binding")
			}
			knowledge, err := kb.FindByChatID(ctx, w.chat.ID)
			if err != nil || knowledge == nil || len(knowledge.TableDescriptions) != 1 || knowledge.TableDescriptions[0].TableName != nextTable || knowledge.TableDescriptions[0].Description != "caller retry selected table" {
				t.Fatal("real retry did not commit KB for its new table selection")
			}
			points, err := vectors.ScrollByFilter(ctx, constants.SchemaCollectionName, []vectordb.FilterCondition{{Key: "chat_id", Value: w.chat.ID.Hex()}}, true)
			if err != nil || len(points) != 1 || points[0].Payload["table_name"] != nextTable || len(points[0].Vector) != embeddings.GetDimension() {
				t.Fatal("real retry did not persist the selected table vector in isolated Qdrant")
			}
			if content, _ := points[0].Payload["content"].(string); !strings.Contains(content, "caller retry field note") {
				t.Error("real persisted vector did not consume the committed caller KB")
			}
			published, err := w.jobs.Get(ctx, job.JobID)
			last, lastErr := w.jobs.LatestSuccess(ctx, job.SourceID)
			if err != nil || !published.ReleaseConfirmed || published.Status != models.DataSyncSucceeded || published.FinishedAt == nil || !published.FinishedAt.Equal(*job.FinishedAt) || lastErr != nil || last.JobID != job.JobID || w.runner.starts.Load() != 1 || modelCalls.Load() != 1 || embeddingCalls.Load() != 1 {
				t.Fatal("real Update retry changed or re-executed its completed predecessor")
			}
			t.Logf("real caller retry completed: scenario=%s selection=%q binding_revision=%d schema_kb_vectors=confirmed original_children=%d", scenario, final.SelectedCollections, final.SourceBindingRevision, w.runner.starts.Load())
		})
	}
}
