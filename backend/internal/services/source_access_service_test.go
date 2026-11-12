package services

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/embedding"
	"yanfachatbi/pkg/llm"
	redisrepo "yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/vectordb"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/sourcegate"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type sourceAccessFixture struct {
	database    *mongo.Database
	client      *mongo.Client
	repo        repositories.ERPSourceRepository
	gate        *sourcegate.Gate
	service     *SourceAccessService
	owner       primitive.ObjectID
	sources     []*models.ERPSource
	connections []models.Connection
	chats       repositories.ChatRepository
}

func newSourceAccessFixture(t *testing.T, count int) *sourceAccessFixture {
	t.Helper()
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("source access persistence unverified: ASSISTANT_TEST_MONGO_URI is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Disconnect(context.Background()) })
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	database := client.Database("source_access_test_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() { database.Drop(context.Background()) })
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })
	repo := repositories.NewERPSourceRepository(database)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.ERPSyncConfig{InstanceID: "source-access-isolated", DeploymentUID: uint32(os.Getuid()), LockDirectory: directory}
	chats := repositories.NewChatRepository(&mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: database.Name()}}, nil)
	service, err := NewSourceAccessService(repo, gate, cfg, chats)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &sourceAccessFixture{database: database, client: client, repo: repo, gate: gate, owner: primitive.NewObjectID(), chats: chats}
	for i := range count {
		port, reader := fmt.Sprint(19000+i), "isolated_reader"
		connection := models.Connection{Type: "postgresql", Host: "127.0.0.1", Port: &port, Username: &reader, Database: "bi_agent"}
		fingerprint := ConnectionFingerprint(connection)
		source, err := service.RegisterApproved(ctx, models.ERPSource{EnvironmentRef: "isolated-only", PhysicalDatabase: "bi_agent",
			ApprovedEndpointAliases: []string{EndpointFingerprint(connection)}, ApprovedConnectionFingerprints: []string{fingerprint}, ReaderRoleRefs: []string{"isolated-reader-ref"}, WriterRoleRef: "isolated-writer-ref",
			ComponentManifestRef: "isolated-component", ApprovalRefs: map[string]string{"Q15-02": "isolated-only-evidence"}, ViewAccountIDs: []primitive.ObjectID{fixture.owner}})
		if err != nil {
			t.Fatal(err)
		}
		// Only this isolated fixture supplies recovery evidence. Registration itself
		// remains recovering; no production recovery/registration is enabled.
		if err := repo.UpdateStateCAS(ctx, source.SourceID, 0, "", repositories.ERPSourceState{Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: cfg.InstanceID}); err != nil {
			t.Fatal(err)
		}
		source, err = repo.Get(ctx, source.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.sources, fixture.connections = append(fixture.sources, source), append(fixture.connections, connection)
		cfg.Associations = append(cfg.Associations, config.ERPAssociation{SourceID: source.SourceID, EndpointFingerprints: []string{EndpointFingerprint(connection)}, ApprovalRef: "isolated-only-evidence"})
	}
	fixture.service, err = NewSourceAccessService(repo, gate, cfg, chats)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *sourceAccessFixture) bindChat(t *testing.T, index int) *models.Chat {
	t.Helper()
	source := f.sources[index]
	chat := models.NewChat(f.owner, f.connections[index], models.DefaultChatSettings())
	chat.ERPAssociated, chat.SourceBindingConfirmed = true, true
	chat.SourceID, chat.SourceBindingRevision = source.SourceID, 1
	chat.SourceConnectionFingerprint = ConnectionFingerprint(chat.Connection)
	binding := models.ERPChatBinding{ChatID: chat.ID, OwnerID: f.owner, ConnectionFingerprint: chat.SourceConnectionFingerprint, BindingRevision: 1, OperationID: uuid.NewString()}
	if err := f.repo.PutBindingCAS(context.Background(), source.SourceID, source.ConfigRevision, nil, binding); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.ConfirmBindingCAS(context.Background(), source.SourceID, source.ConfigRevision, binding); err != nil {
		t.Fatal(err)
	}
	if err := f.chats.Create(chat); err != nil {
		t.Fatal(err)
	}
	return chat
}

type sourceAccessEffects struct {
	fixture    *sourceAccessFixture
	chatID     primitive.ObjectID
	probeError error
	started    chan struct{}
	release    chan struct{}
	probeCalls atomic.Int32
}

func (e *sourceAccessEffects) checkExclusion() error {
	for _, source := range e.fixture.sources {
		lease, err := e.fixture.gate.Acquire(context.Background(), sourcegate.Target{SourceID: source.SourceID, Device: source.LockDevice, Inode: source.LockInode}, sourcegate.Exclusive,
			sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
		if lease != nil {
			lease.Close()
		}
		if !errors.Is(err, sourcegate.ErrBusy) {
			return fmt.Errorf("binding lost EX: %v", err)
		}
	}
	return nil
}

func (e *sourceAccessEffects) Probe(ctx context.Context, connection models.Connection) error {
	e.probeCalls.Add(1)
	chat, err := e.fixture.chats.ReadCurrent(ctx, e.fixture.owner, e.chatID)
	if err != nil {
		return err
	}
	if chat.SourceBindingConfirmed {
		return errors.New("probe exposed a confirmed half-binding")
	}
	if err := e.fixture.service.RequireRead(ctx, e.fixture.owner, chat, connection); err != nil {
		return err
	}
	if err := e.checkExclusion(); err != nil {
		return err
	}
	return e.probeError
}

func (e *sourceAccessEffects) Invalidate(ctx context.Context, previous *models.Chat) error {
	if err := e.fixture.service.RequireRead(ctx, e.fixture.owner, previous, previous.Connection); err != nil {
		return err
	}
	stored, err := e.fixture.chats.ReadCurrent(ctx, e.fixture.owner, e.chatID)
	if err != nil {
		return err
	}
	if stored.SourceBindingRevision <= previous.SourceBindingRevision {
		return errors.New("cache invalidated before persisted binding")
	}
	return e.checkExclusion()
}

func (e *sourceAccessEffects) Refresh(ctx context.Context, chat *models.Chat) error {
	if err := e.fixture.service.RequireRead(ctx, e.fixture.owner, chat, chat.Connection); err != nil {
		return err
	}
	if err := e.checkExclusion(); err != nil {
		return err
	}
	if e.started != nil {
		close(e.started)
	}
	if e.release != nil {
		<-e.release
	}
	return ctx.Err()
}

func waitSourceAccess(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("actual source worker did not reach its barrier")
	}
}

func TestSourceAccessIdentityAndIndependentIsolation(t *testing.T) {
	f := newSourceAccessFixture(t, 1)
	chat := f.bindChat(t, 0)
	read, err := f.service.AcquireRead(context.Background(), f.owner, chat, chat.Connection)
	if err != nil {
		t.Fatal(err)
	}
	if read.Lease.Mode() != sourcegate.Shared {
		t.Fatal("ordinary source root did not acquire SH")
	}
	other, err := f.service.AcquireRead(context.Background(), f.owner, chat, chat.Connection)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	nested, err := f.service.AcquireRead(read.Context, f.owner, chat, chat.Connection)
	if err != nil {
		t.Fatal(err)
	}
	if nested.Lease.Identity().OperationID != read.Lease.Identity().OperationID {
		t.Fatal("nested read created a different operation")
	}
	read.Close()
	if err := f.service.RequireRead(nested.Context, f.owner, chat, chat.Connection); err != nil {
		t.Fatal(err)
	}
	nested.Close()
	if _, err := f.service.AcquireRead(read.Context, f.owner, chat, chat.Connection); !errors.Is(err, sourcegate.ErrClosed) {
		t.Fatalf("closed inherited lease reacquired a root: %v", err)
	}
	if err := f.service.RequireRead(context.Background(), f.owner, chat, chat.Connection); !errors.Is(err, sourcegate.ErrMissing) {
		t.Fatalf("missing-lease low-level read: %v", err)
	}
	if f.service.CanOperate(f.sources[0], f.owner) {
		t.Fatal("view permission implicitly granted sync operation")
	}
	copy := *chat
	copy.ID, copy.DisplayName, copy.SourceBindingConfirmed = primitive.NewObjectID(), "renamed", false
	if err := f.chats.Create(&copy); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcquireRead(context.Background(), f.owner, &copy, copy.Connection); !errors.Is(err, ErrSourceUnresolved) {
		t.Fatalf("copied Chat bypassed own binding: %v", err)
	}
	changedReader := chat.Connection
	unapprovedReader := "another_reader"
	changedReader.Username = &unapprovedReader
	newChat := models.NewChat(f.owner, changedReader, models.DefaultChatSettings())
	if err := f.chats.Create(newChat); err != nil {
		t.Fatal(err)
	}
	if classification, _ := f.service.Classify(newChat, changedReader); classification != models.SourceRegisteredERP {
		t.Fatal("reader change hid an approved ERP endpoint")
	}
	if _, err := f.service.AcquireRead(context.Background(), f.owner, newChat, changedReader); !errors.Is(err, ErrSourceUnresolved) {
		t.Fatalf("new reader bypassed ERP binding: %v", err)
	}
	changed := chat.Connection
	changed.Host = "unapproved.invalid"
	if classification, _ := f.service.Classify(chat, changed); classification != models.SourceUnresolvedERP {
		t.Fatal("changed endpoint downgraded ERP lineage")
	}
	candidate := models.NewChat(f.owner, changed, models.DefaultChatSettings())
	fingerprint := EndpointFingerprint(changed)
	candidateService, err := NewSourceAccessService(f.repo, f.gate, config.ERPSyncConfig{InstanceID: "source-access-isolated", DeploymentUID: uint32(os.Getuid()), LockDirectory: f.gate.Directory(),
		Associations: []config.ERPAssociation{{EndpointFingerprints: []string{fingerprint}, ApprovalRef: "controlled-isolated-candidate"}}}, f.chats)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := candidateService.AcquireRead(context.Background(), f.owner, candidate, changed); !errors.Is(err, ErrSourceUnresolved) {
		t.Fatalf("unresolved controlled candidate admitted: %v", err)
	}
	// A same-name, same-driver, localhost database on a different endpoint is not ERP.
	independentConnection := chat.Connection
	port := "19999"
	independentConnection.Port = &port
	independent := models.NewChat(f.owner, independentConnection, models.DefaultChatSettings())
	for _, phase := range []models.ERPSourcePhase{models.ERPSourceSyncing, models.ERPSourceRecovering} {
		source, err := f.repo.Get(context.Background(), chat.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		operation := &models.ERPSourceOperation{OperationID: uuid.NewString(), Kind: "isolated-maintenance"}
		currentOperation := ""
		if source.ActiveOperation != nil {
			currentOperation = source.ActiveOperation.OperationID
		}
		if err := f.repo.UpdateStateCAS(context.Background(), source.SourceID, source.StateRevision, currentOperation, repositories.ERPSourceState{Phase: phase, ActiveOperation: operation}); err != nil {
			t.Fatal(err)
		}
		read, err := f.service.AcquireRead(context.Background(), f.owner, independent, independentConnection)
		if err != nil || read.Classification != models.SourceIndependent || read.Lease != nil {
			t.Fatalf("independent gated by ERP %s: %v", phase, err)
		}
		want := ErrSourceRecovering
		if phase == models.ERPSourceSyncing {
			want = ErrSourceSyncing
		}
		if _, err := f.service.AcquireRead(context.Background(), f.owner, chat, chat.Connection); !errors.Is(err, want) {
			t.Fatalf("ERP state veto ignored: %v", err)
		}
	}
	// Disconnected real Mongo client proves the independent path has no ERP IO.
	if err := f.database.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	read, err = f.service.AcquireRead(context.Background(), f.owner, independent, independentConnection)
	if err != nil || read.Classification != models.SourceIndependent {
		t.Fatalf("ERP storage outage disabled independent source: %v", err)
	}
	if _, err := f.service.AcquireRead(context.Background(), f.owner, chat, chat.Connection); !errors.Is(err, ErrSourceStateUnavailable) {
		t.Fatalf("ERP storage outage admitted source: %v", err)
	}
}

func TestSourceAccessBindingRetainsOwnEXAfterReturn(t *testing.T) {
	f := newSourceAccessFixture(t, 2)
	previous := f.bindChat(t, 0)
	next := *previous
	next.Connection = f.connections[1]
	effects := &sourceAccessEffects{fixture: f, chatID: previous.ID, started: make(chan struct{}), release: make(chan struct{})}
	change, err := f.service.ChangeBinding(context.Background(), f.owner, previous, &next, next.Connection, f.chats, effects)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-effects.release:
		default:
			close(effects.release)
		}
		_ = change.Wait()
	})
	waitSourceAccess(t, effects.started)
	if err := effects.checkExclusion(); err != nil {
		t.Fatal(err)
	}
	stored, err := f.chats.ReadCurrent(context.Background(), f.owner, previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcquireRead(context.Background(), f.owner, stored, next.Connection); !errors.Is(err, sourcegate.ErrBusy) {
		t.Fatalf("HTTP return released refresh EX: %v", err)
	}
	close(effects.release)
	if err := change.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := change.Wait(); err != nil {
		t.Fatalf("completion not stable: %v", err)
	}
	read, err := f.service.AcquireRead(context.Background(), f.owner, stored, next.Connection)
	if err != nil {
		t.Fatalf("completed binding is not executable: %v", err)
	}
	read.Close()
	oldSource, err := f.repo.Get(context.Background(), previous.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range oldSource.ChatBindings {
		if binding.ChatID == previous.ID {
			t.Fatal("old source retained obsolete Chat binding")
		}
	}
}

func TestSourceAccessBindingCancellationWaitsForActualChild(t *testing.T) {
	f := newSourceAccessFixture(t, 1)
	previous := f.bindChat(t, 0)
	next := *previous
	effects := &sourceAccessEffects{fixture: f, chatID: previous.ID, started: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	change, err := f.service.ChangeBinding(ctx, f.owner, previous, &next, next.Connection, f.chats, effects)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-effects.release:
		default:
			close(effects.release)
		}
		_ = change.Wait()
	})
	waitSourceAccess(t, effects.started)
	cancel()
	if err := effects.checkExclusion(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-change.done:
		t.Fatal("cancellation marked a running child complete")
	default:
	}
	close(effects.release)
	if err := change.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation result: %v", err)
	}
	stored, err := f.chats.ReadCurrent(context.Background(), f.owner, previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcquireRead(context.Background(), f.owner, stored, next.Connection); !errors.Is(err, ErrSourceRecovering) {
		t.Fatalf("canceled binding lost recovery veto: %v", err)
	}
}

type sourceAccessCommitFault struct {
	BindingChatStore
	writeFirst bool
}

func (s sourceAccessCommitFault) CommitBindingCAS(ctx context.Context, before, after *models.Chat) error {
	if s.writeFirst {
		if err := s.BindingChatStore.CommitBindingCAS(ctx, before, after); err != nil {
			return err
		}
	}
	return errors.New("isolated commit acknowledgement interrupted")
}

func TestSourceAccessBindingFailureNeverExposesHalfBinding(t *testing.T) {
	for _, failure := range []string{"probe", "commit-before-write", "commit-after-write"} {
		t.Run(failure, func(t *testing.T) {
			f := newSourceAccessFixture(t, 1)
			previous := f.bindChat(t, 0)
			next := *previous
			effects := &sourceAccessEffects{fixture: f, chatID: previous.ID}
			var store BindingChatStore = f.chats
			if failure == "probe" {
				effects.probeError = errors.New("isolated probe failed")
			} else {
				store = sourceAccessCommitFault{BindingChatStore: f.chats, writeFirst: failure == "commit-after-write"}
			}
			change, err := f.service.ChangeBinding(context.Background(), f.owner, previous, &next, next.Connection, store, effects)
			if err == nil || change != nil {
				t.Fatal("interrupted binding succeeded")
			}
			stored, err := f.chats.ReadCurrent(context.Background(), f.owner, previous.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !stored.ERPAssociated || stored.SourceBindingRevision <= previous.SourceBindingRevision {
				t.Fatal("failed binding erased ERP lineage")
			}
			if read, err := f.service.AcquireRead(context.Background(), f.owner, stored, next.Connection); err == nil {
				read.Close()
				t.Fatal("failed binding remained executable")
			}
			source, err := f.repo.Get(context.Background(), previous.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			if source.Phase != models.ERPSourceRecovering || source.ActiveOperation == nil {
				t.Fatal("failure lost durable recovery veto")
			}
			// Actual EX was closed after synchronous failure; recovery, not a map or
			// leaked FD, now vetoes execution until a recovery owner verifies it.
			lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: source.SourceID, Device: source.LockDevice, Inode: source.LockInode}, sourcegate.Exclusive,
				sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
			if err != nil {
				t.Fatalf("failure leaked EX: %v", err)
			}
			lease.Close()
		})
	}
}

func TestSourceAccessSortedMultiSourceFailureReleasesEarlierEX(t *testing.T) {
	f := newSourceAccessFixture(t, 2)
	order := []int{0, 1}
	sort.Slice(order, func(i, j int) bool { return f.sources[order[i]].SourceID < f.sources[order[j]].SourceID })
	previous := f.bindChat(t, order[0])
	blocked := f.sources[order[1]]
	blocker, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: blocked.SourceID, Device: blocked.LockDevice, Inode: blocked.LockInode}, sourcegate.Exclusive,
		sourcegate.Identity{SourceID: blocked.SourceID, OperationID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	next := *previous
	next.Connection = f.connections[order[1]]
	effects := &sourceAccessEffects{fixture: f, chatID: previous.ID}
	if change, err := f.service.ChangeBinding(context.Background(), f.owner, previous, &next, next.Connection, f.chats, effects); !errors.Is(err, sourcegate.ErrBusy) || change != nil {
		t.Fatalf("contended old/new EX result: %v", err)
	}
	if effects.probeCalls.Load() != 0 {
		t.Fatal("probe ran before acquiring every EX")
	}
	first := f.sources[order[0]]
	lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: first.SourceID, Device: first.LockDevice, Inode: first.LockInode}, sourcegate.Exclusive,
		sourcegate.Identity{SourceID: first.SourceID, OperationID: uuid.NewString()})
	if err != nil {
		t.Fatalf("earlier sorted EX leaked: %v", err)
	}
	lease.Close()
	stored, err := f.chats.ReadCurrent(context.Background(), f.owner, previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameChatBinding(previous, stored) {
		t.Fatal("lock failure mutated the binding")
	}
}

type sourceAccessDescendantEffects struct {
	*sourceAccessEffects
	descendantRelease chan struct{}
	refreshReturned   chan struct{}
}

func (e *sourceAccessDescendantEffects) Refresh(ctx context.Context, chat *models.Chat) error {
	if err := e.fixture.service.RequireRead(ctx, e.fixture.owner, chat, chat.Connection); err != nil {
		return err
	}
	identity, err := sourcegate.ContextIdentity(ctx, chat.SourceID)
	if err != nil {
		return err
	}
	_, child, err := sourcegate.RetainContext(ctx, identity)
	if err != nil {
		return err
	}
	go func() { defer child.Close(); <-e.descendantRelease }()
	close(e.refreshReturned)
	return nil
}

func TestSourceAccessBindingJoinsRetainedGrandchild(t *testing.T) {
	f := newSourceAccessFixture(t, 1)
	previous := f.bindChat(t, 0)
	next := *previous
	effects := &sourceAccessDescendantEffects{sourceAccessEffects: &sourceAccessEffects{fixture: f, chatID: previous.ID},
		descendantRelease: make(chan struct{}), refreshReturned: make(chan struct{})}
	change, err := f.service.ChangeBinding(context.Background(), f.owner, previous, &next, next.Connection, f.chats, effects)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-effects.descendantRelease:
		default:
			close(effects.descendantRelease)
		}
		_ = change.Wait()
	})
	waitSourceAccess(t, effects.refreshReturned)
	if err := effects.checkExclusion(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-change.done:
		t.Fatal("refresh return completed a live retained grandchild")
	default:
	}
	close(effects.descendantRelease)
	if err := change.Wait(); err != nil {
		t.Fatal(err)
	}
	stored, err := f.chats.ReadCurrent(context.Background(), f.owner, previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.service.AcquireRead(context.Background(), f.owner, stored, stored.Connection)
	if err != nil {
		t.Fatal(err)
	}
	read.Close()
}

type sourceAccessConfirmFault struct {
	repositories.ERPSourceRepository
}

func (r sourceAccessConfirmFault) ConfirmBindingCAS(ctx context.Context, id string, revision int64, binding models.ERPChatBinding) error {
	if err := r.ERPSourceRepository.ConfirmBindingCAS(ctx, id, revision, binding); err != nil {
		return err
	}
	return errors.New("isolated confirmation acknowledgement lost after durable write")
}

func TestSourceAccessBindingUncertainConfirmationKeepsRecoveryVeto(t *testing.T) {
	f := newSourceAccessFixture(t, 1)
	previous := f.bindChat(t, 0)
	next := *previous
	f.service.repo = sourceAccessConfirmFault{ERPSourceRepository: f.repo}
	effects := &sourceAccessEffects{fixture: f, chatID: previous.ID}
	if change, err := f.service.ChangeBinding(context.Background(), f.owner, previous, &next, next.Connection, f.chats, effects); err == nil || change != nil {
		t.Fatal("lost confirmation acknowledgement published a completed binding")
	}
	stored, err := f.chats.ReadCurrent(context.Background(), f.owner, previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcquireRead(context.Background(), f.owner, stored, stored.Connection); !errors.Is(err, ErrSourceRecovering) {
		t.Fatalf("matching documents bypassed uncertain operation recovery: %v", err)
	}
}

func TestSourceAccessProductionStoreFencesStaleWrites(t *testing.T) {
	f := newSourceAccessFixture(t, 1)
	ctx := context.Background()
	previous := f.bindChat(t, 0)
	next := *previous
	effects := &sourceAccessEffects{fixture: f, chatID: previous.ID, started: make(chan struct{}), release: make(chan struct{})}
	change, err := f.service.ChangeBinding(ctx, f.owner, previous, &next, next.Connection, f.chats, effects)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-effects.release:
		default:
			close(effects.release)
		}
		change.Wait()
	})
	waitSourceAccess(t, effects.started)
	name, collections := "metadata racing binding", "ALL"
	if err := f.chats.PatchMetadata(ctx, f.owner, previous.ID, repositories.ChatMetadataPatch{DisplayName: &name, SelectedCollections: &collections}); err != nil {
		t.Fatal(err)
	}
	if err := f.chats.UpdateConnectionSchema(ctx, previous, "schema from the previous connection"); !errors.Is(err, ErrSourceBindingConflict) {
		t.Fatalf("old schema crossed the binding fence: %v", err)
	}
	if err := f.chats.PatchMetadata(ctx, primitive.NewObjectID(), previous.ID, repositories.ChatMetadataPatch{DisplayName: &name}); !errors.Is(err, ErrSourceBindingConflict) {
		t.Fatalf("foreign metadata write: %v", err)
	}
	stored, err := f.chats.ReadCurrent(ctx, f.owner, previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DisplayName != name || stored.SourceBindingRevision != previous.SourceBindingRevision+1 || !stored.SourceBindingConfirmed || stored.SourceID != previous.SourceID || stored.Connection.CurrentSchema != nil {
		t.Fatalf("stale metadata/schema replaced the new binding: %+v", stored)
	}
	if _, err := f.service.ChangeBinding(ctx, f.owner, previous, &next, next.Connection, f.chats, effects); !errors.Is(err, sourcegate.ErrBusy) {
		t.Fatalf("second writer entered a live binding: %v", err)
	}
	close(effects.release)
	if err := change.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ChangeBinding(ctx, f.owner, previous, &next, next.Connection, f.chats, effects); !errors.Is(err, ErrSourceBindingConflict) {
		t.Fatalf("stale writer overwrote committed binding: %v", err)
	}
	reopened := repositories.NewChatRepository(&mongodb.MongoDBClient{Client: f.client, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}, nil)
	reloaded, err := reopened.FindByID(previous.ID)
	if err != nil || !sameChatBinding(stored, reloaded) || reloaded.DisplayName != name {
		t.Fatalf("primary reload: %v, %+v", err, reloaded)
	}
}

func TestSourceAccessProductionIndependentConnectionCAS(t *testing.T) {
	f := newSourceAccessFixture(t, 1)
	ctx := context.Background()
	connection := f.connections[0]
	connection.Host = "unrelated.invalid"
	chat := models.NewChat(f.owner, connection, models.DefaultChatSettings())
	if err := f.chats.Create(chat); err != nil {
		t.Fatal(err)
	}
	next := connection
	next.Database = "another_independent_database"
	if err := f.chats.UpdateIndependentConnectionCAS(ctx, chat, next); err != nil {
		t.Fatal(err)
	}
	if err := f.chats.UpdateIndependentConnectionCAS(ctx, chat, connection); !errors.Is(err, ErrSourceBindingConflict) {
		t.Fatalf("competing stale connection won: %v", err)
	}
	if err := f.chats.UpdateConnectionSchema(ctx, chat, "old independent schema"); !errors.Is(err, ErrSourceBindingConflict) {
		t.Fatalf("old independent schema survived connection change: %v", err)
	}
	stored, err := f.chats.ReadCurrent(ctx, f.owner, chat.ID)
	if err != nil || stored.Connection.Database != next.Database || stored.ConnectionRevision != 1 {
		t.Fatalf("independent CAS result: %v %+v", err, stored)
	}
}

type sourceAccessTerminal struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newSourceAccessTerminal() *sourceAccessTerminal {
	return &sourceAccessTerminal{started: make(chan struct{}), release: make(chan struct{})}
}
func (b *sourceAccessTerminal) enter() { b.once.Do(func() { close(b.started); <-b.release }) }
func (b *sourceAccessTerminal) finish() {
	select {
	case <-b.release:
	default:
		close(b.release)
	}
}

// Pause after classifying the captured connection; all admission and rebinding
// still use the production policy, repositories, gate and PostgreSQL driver.
type sourceAccessPausedPolicy struct {
	*SourceAccessService
	chatID  primitive.ObjectID
	armed   atomic.Bool
	barrier *sourceAccessTerminal
}

func (p *sourceAccessPausedPolicy) Classify(chat *models.Chat, connection models.Connection) (models.SourceClassification, string) {
	classification, sourceID := p.SourceAccessService.Classify(chat, connection)
	if chat != nil && chat.ID == p.chatID && classification == models.SourceIndependent && p.armed.CompareAndSwap(true, false) {
		p.barrier.enter()
	}
	return classification, sourceID
}

// Transparent gRPC forwarding keeps the production Qdrant client and a real
// isolated Qdrant store. The barrier delays the actual upstream completion reply.
type sourceAccessWireCodec struct{}

func (sourceAccessWireCodec) Name() string                          { return "proto" }
func (sourceAccessWireCodec) Marshal(v interface{}) ([]byte, error) { return *v.(*[]byte), nil }
func (sourceAccessWireCodec) Unmarshal(data []byte, v interface{}) error {
	destination := v.(*[]byte)
	*destination = append((*destination)[:0], data...)
	return nil
}

func TestSourceAccessProductionEffectsTerminals(t *testing.T) {
	pgURL, redisAddress, vectorAddress := os.Getenv("SOURCE_ACCESS_TEST_PG_URL"), os.Getenv("SOURCE_ACCESS_TEST_REDIS_ADDR"), os.Getenv("SOURCE_ACCESS_TEST_QDRANT_ADDR")
	if pgURL == "" || redisAddress == "" || vectorAddress == "" {
		t.Skip("production effects unverified: explicit isolated PG URL, Redis and Qdrant addresses are required")
	}
	f := newSourceAccessFixture(t, 1)
	ctx := context.Background()
	parsed, err := url.Parse(pgURL)
	if err != nil || parsed.User == nil || parsed.Hostname() == "" || strings.TrimPrefix(parsed.Path, "/") != "bi_agent" {
		t.Fatal("SOURCE_ACCESS_TEST_PG_URL must identify the isolated bi_agent fixture database")
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	reader, password := parsed.User.Username(), ""
	if value, ok := parsed.User.Password(); ok {
		password = value
	}
	connection := models.Connection{Type: "postgresql", Host: parsed.Hostname(), Port: &port, Username: &reader, Password: &password, Database: "bi_agent"}
	pgDestination := net.JoinHostPort(connection.Host, port)
	pgListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pgListener.Close() })
	var pgSessions atomic.Int32
	go func() {
		for {
			client, err := pgListener.Accept()
			if err != nil {
				return
			}
			pgSessions.Add(1)
			go func() {
				upstream, err := net.Dial("tcp", pgDestination)
				if err != nil {
					client.Close()
					return
				}
				finished := make(chan struct{})
				go func() { io.Copy(upstream, client); upstream.Close(); client.Close(); close(finished) }()
				io.Copy(client, upstream)
				client.Close()
				upstream.Close()
				<-finished
			}()
		}
	}()
	proxyPGHost, proxyPGPort, _ := net.SplitHostPort(pgListener.Addr().String())
	connection.Host, connection.Port = proxyPGHost, &proxyPGPort
	control, err := sql.Open("postgres", pgURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	tableName := "source_access_" + primitive.NewObjectID().Hex()
	if _, err := control.ExecContext(ctx, `CREATE TABLE public."`+tableName+`" (id integer PRIMARY KEY, label text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.ExecContext(context.Background(), `DROP TABLE public."`+tableName+`"`) })
	if _, err := control.ExecContext(ctx, `INSERT INTO public."`+tableName+`" VALUES (1, 'isolated example')`); err != nil {
		t.Fatal(err)
	}
	source := *f.sources[0]
	alias := connection.Clone()
	alias.Host = "localhost"
	source.ApprovedEndpointAliases = []string{EndpointFingerprint(connection), EndpointFingerprint(alias)}
	source.ApprovedConnectionFingerprints = []string{ConnectionFingerprint(connection), ConnectionFingerprint(alias)}
	source.ConfigRevision++
	if err := f.repo.UpdateConfigCAS(ctx, source.SourceID, f.sources[0].ConfigRevision, &source); err != nil {
		t.Fatal(err)
	}
	f.sources[0], err = f.repo.Get(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	f.connections[0] = connection
	f.service, err = NewSourceAccessService(f.repo, f.gate, config.ERPSyncConfig{InstanceID: "source-access-isolated", DeploymentUID: uint32(os.Getuid()), LockDirectory: f.gate.Directory(),
		Associations: []config.ERPAssociation{{SourceID: source.SourceID, EndpointFingerprints: source.ApprovedEndpointAliases, ApprovalRef: "isolated-only-evidence"}}}, f.chats)
	if err != nil {
		t.Fatal(err)
	}
	modelBarrier, embeddingBarrier, vectorBarrier := newSourceAccessTerminal(), newSourceAccessTerminal(), newSourceAccessTerminal()
	modelEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/embeddings") {
			embeddingBarrier.enter()
			var input struct {
				Input      []string `json:"input"`
				Dimensions int      `json:"dimensions"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(w, "invalid embedding request", 400)
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
			json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data, "model": "text-embedding-3-small"})
			return
		}
		modelBarrier.enter()
		content, _ := json.Marshal(map[string]interface{}{"tables": []map[string]interface{}{{"table_name": tableName, "description": "isolated table", "field_descriptions": []map[string]string{{"field_name": "label", "description": "isolated newly generated label"}}}}})
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "isolated-kb", "object": "chat.completion", "choices": []map[string]interface{}{{"index": 0, "message": map[string]string{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(modelEndpoint.Close)
	upstream, err := grpc.NewClient(vectorAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := grpc.NewServer(grpc.ForceServerCodec(sourceAccessWireCodec{}), grpc.UnknownServiceHandler(func(_ interface{}, stream grpc.ServerStream) error {
		method, ok := grpc.MethodFromServerStream(stream)
		if !ok {
			return errors.New("missing gRPC method")
		}
		var request, response []byte
		if err := stream.RecvMsg(&request); err != nil {
			return err
		}
		if err := upstream.Invoke(stream.Context(), method, &request, &response, grpc.ForceCodec(sourceAccessWireCodec{})); err != nil {
			return err
		}
		if strings.HasSuffix(method, "/Upsert") {
			vectorBarrier.enter()
		}
		return stream.SendMsg(&response)
	}))
	go proxy.Serve(listener)
	t.Cleanup(proxy.Stop)
	redisClient := goredis.NewClient(&goredis.Options{Addr: redisAddress})
	t.Cleanup(func() { redisClient.Close() })
	redisStore := redisrepo.NewRedisRepositories(redisClient)
	f.chats = repositories.NewChatRepository(&mongodb.MongoDBClient{Client: f.client, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}, redisStore)
	f.service.chats = f.chats
	manager, err := dbmanager.NewManager(redisStore, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	model, err := llm.NewOpenAIClient(llm.Config{APIKey: "isolated-only", BaseURL: modelEndpoint.URL + "/v1", Model: "isolated-kb"})
	if err != nil {
		t.Fatal(err)
	}
	embeddings, err := embedding.NewOpenAIProvider("isolated-only", "text-embedding-3-small", modelEndpoint.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	proxyHost, proxyPort, _ := net.SplitHostPort(listener.Addr().String())
	vectors, err := vectordb.NewQdrantClient(vectordb.QdrantConfig{Host: proxyHost, Port: proxyPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vectors.Close() })
	vectorization := NewVectorizationService(embeddings, vectors)
	if err := vectorization.EnsureReady(ctx); err != nil {
		t.Fatal(err)
	}
	kbWriteBarrier, cancelKBBarrier := newSourceAccessTerminal(), newSourceAccessTerminal()
	var blockKBWrite, armCancelKBWrite atomic.Bool
	kbClient, err := mongo.Connect(ctx, options.Client().ApplyURI(os.Getenv("ASSISTANT_TEST_MONGO_URI")).SetMonitor(&event.CommandMonitor{
		Started: func(_ context.Context, command *event.CommandStartedEvent) {
			if command.DatabaseName == f.database.Name() && command.CommandName == "update" && command.Command.Lookup("update").StringValue() == "knowledge_bases" {
				if blockKBWrite.CompareAndSwap(true, false) {
					kbWriteBarrier.enter()
				} else if armCancelKBWrite.CompareAndSwap(true, false) {
					cancelKBBarrier.enter()
				}
			}
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kbClient.Disconnect(context.Background()) })
	kb := repositories.NewKnowledgeBaseRepository(&mongodb.MongoDBClient{Client: kbClient, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}})
	oldKey, oldDataKey, oldMax := config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey, config.Env.MaxChatsPerUser
	config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey, config.Env.MaxChatsPerUser = "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef", 1
	t.Cleanup(func() {
		config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey, config.Env.MaxChatsPerUser = oldKey, oldDataKey, oldMax
	})
	service := NewChatService(f.chats, manager, model, nil, redisStore, nil, vectorization, kb, nil, nil, f.service).(*chatService)
	manager.SetStreamHandler(service)
	previous := f.bindChat(t, 0)
	previous.SelectedCollections = tableName
	if err := f.chats.PatchMetadata(ctx, f.owner, previous.ID, repositories.ChatMetadataPatch{SelectedCollections: &tableName}); err != nil {
		t.Fatal(err)
	}
	// bindChat stores an empty selection, so this PATCH is a value change that
	// advances the selection fencing revision; mirror it before any later CAS.
	previous.SelectionRevision++
	initialKnowledge := models.NewKnowledgeBase(previous.ID)
	initialKnowledge.TableDescriptions = []models.TableDescription{{TableName: tableName, FieldDescriptions: []models.FieldDescription{{FieldName: "id", Description: "preserved old id note"}}}}
	if err := kb.Upsert(ctx, initialKnowledge); err != nil {
		t.Fatal(err)
	}
	staleKnowledge := models.NewKnowledgeBase(previous.ID)
	staleKnowledge.TableDescriptions = []models.TableDescription{{TableName: tableName, Description: "stale Redis description", FieldDescriptions: []models.FieldDescription{{FieldName: "label", Description: "stale Redis label"}}}}
	staleKB, err := json.Marshal(staleKnowledge)
	if err != nil {
		t.Fatal(err)
	}
	legacyKBKey := "kb:chat:" + previous.ID.Hex()
	if err := redisStore.SetCompressed(legacyKBKey, staleKB, time.Hour, ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { redisStore.Del(legacyKBKey, context.Background()) })
	blockKBWrite.Store(true)
	next := *previous
	var change *BindingChange
	t.Cleanup(func() {
		modelBarrier.finish()
		embeddingBarrier.finish()
		kbWriteBarrier.finish()
		vectorBarrier.finish()
		if change != nil {
			change.Wait()
		}
		vectorization.DeleteChatVectors(ctx, previous.ID.Hex())
	})
	blockedSQL, err := control.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blockedSQL.Rollback() })
	if _, err := blockedSQL.ExecContext(ctx, `LOCK TABLE public."`+tableName+`" IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	change, err = f.service.ChangeBinding(ctx, f.owner, previous, &next, connection, f.chats, &chatBindingEffects{service: service, owner: f.owner, chatID: previous.ID, probe: true, refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	// The spec removed table sampling from refresh, so no source query blocks on
	// the EX lock anymore: the catalog fetch completes under it and could not
	// have read the locked table. The pooled source-identity session proves the
	// refresh reached PostgreSQL; the model terminal holds the pipeline.
	waitSourceAccessSession(t, control, source.SourceID)
	waitSourceAccess(t, modelBarrier.started)
	if err := (&sourceAccessEffects{fixture: f}).checkExclusion(); err != nil {
		t.Fatal(err)
	}
	if err := blockedSQL.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, barrier := range []*sourceAccessTerminal{modelBarrier, kbWriteBarrier, embeddingBarrier, vectorBarrier} {
		waitSourceAccess(t, barrier.started)
		if err := (&sourceAccessEffects{fixture: f}).checkExclusion(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-change.done:
			t.Fatal("binding completed before a real terminal reply")
		default:
		}
		if _, err := manager.GetConnection(previous.ID.Hex()); !errors.Is(err, sourcegate.ErrMissing) {
			t.Fatalf("ERP raw executor escape: %v", err)
		}
		info, exists := manager.GetConnectionInfo(previous.ID.Hex())
		if !exists || info.DB != nil {
			t.Fatal("ERP metadata getter exposed a SQL pool")
		}
		if barrier == modelBarrier {
			name := "name-only while own EX is held"
			if _, _, err := service.Update(f.owner.Hex(), previous.ID.Hex(), &dtos.UpdateChatRequest{DisplayName: &name}); err != nil {
				t.Fatalf("name-only update locked or probed: %v", err)
			}
		}
		if barrier == kbWriteBarrier {
			var pending models.KnowledgeBase
			if err := f.database.Collection("knowledge_bases").FindOne(ctx, map[string]interface{}{"chat_id": previous.ID}).Decode(&pending); err != nil {
				t.Fatal(err)
			}
			if table := pending.GetTableDescription(tableName); table == nil || table.Description != "" || len(table.FieldDescriptions) != 1 || table.FieldDescriptions[0].FieldName != "id" {
				t.Fatal("KB persistence barrier did not preserve the old primary snapshot before its update")
			}
		}
		if barrier == embeddingBarrier {
			// A legacy writer arrives after the new Mongo KB has committed.
			if err := redisStore.SetCompressed(legacyKBKey, staleKB, time.Hour, ctx); err != nil {
				t.Fatal(err)
			}
			current, err := kb.FindByChatID(ctx, previous.ID)
			if err != nil || current == nil || current.GetTableDescription(tableName) == nil || current.GetTableDescription(tableName).Description != "isolated table" {
				t.Fatalf("late legacy KB write resurrected an old object: %v", err)
			}
		}
		barrier.finish()
	}
	if err := change.Wait(); err != nil {
		t.Fatal(err)
	}
	stored, err := f.chats.ReadCurrent(ctx, f.owner, previous.ID)
	if err != nil || stored.Connection.CurrentSchema == nil || !strings.Contains(*stored.Connection.CurrentSchema, tableName) {
		t.Fatalf("production schema was not saved: %v", err)
	}
	knowledge, err := kb.FindByChatID(ctx, previous.ID)
	if err != nil || knowledge == nil || len(knowledge.TableDescriptions) != 1 || knowledge.TableDescriptions[0].Description != "isolated table" {
		t.Fatalf("real KB result: %v %+v", err, knowledge)
	}
	fieldDescriptions := make(map[string]string)
	for _, field := range knowledge.TableDescriptions[0].FieldDescriptions {
		fieldDescriptions[field.FieldName] = field.Description
	}
	if fieldDescriptions["id"] != "preserved old id note" || fieldDescriptions["label"] != "isolated newly generated label" {
		t.Fatal("production KB did not retain the old field and persist the new field description")
	}
	points, err := vectors.ScrollByFilter(ctx, constants.SchemaCollectionName, []vectordb.FilterCondition{{Key: "chat_id", Value: previous.ID.Hex()}}, true)
	if err != nil || len(points) != 1 || points[0].Payload["table_name"] != tableName || len(points[0].Vector) != embeddings.GetDimension() {
		for _, point := range points {
			t.Logf("persisted table=%v vector dimensions=%d", point.Payload["table_name"], len(point.Vector))
		}
		t.Fatalf("actual persisted vector differs from the selected schema: error=%v points=%d expected table=%s dimensions=%d", err, len(points), tableName, embeddings.GetDimension())
	}
	if content, _ := points[0].Payload["content"].(string); !strings.Contains(content, "isolated newly generated label") || !strings.Contains(content, "preserved old id note") || strings.Contains(content, "stale Redis label") {
		t.Fatal("vector payload did not use the newly committed production KB descriptions")
	}
	canceledKBContext, cancelKBRead := context.WithCancel(ctx)
	cancelKBRead()
	if _, err := kb.FindByChatID(canceledKBContext, previous.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("KB read ignored caller cancellation or returned a legacy cached object: %v", err)
	}
	// Stored-schema access for an ERP chat is source-gated; read it under a
	// real SH lease the way production callers do.
	schemaRead, err := f.service.AcquireRead(ctx, f.owner, stored, connection)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := manager.GetSchemaManager().GetStoredSchemaStorage(schemaRead.Context, previous.ID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	legacy := *storage
	legacy.FullSchema = &dbmanager.SchemaInfo{Tables: map[string]dbmanager.TableSchema{"stale_legacy_table": {Name: "stale_legacy_table"}}}
	legacyStore, err := dbmanager.NewSchemaStorageService(redisStore, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.Store(ctx, previous.ID.Hex(), &legacy); err != nil {
		t.Fatal(err)
	}
	currentSchema, err := manager.GetSchemaManager().GetStoredSchemaInfo(schemaRead.Context, previous.ID.Hex())
	if err != nil || currentSchema.Tables[tableName].Name != tableName {
		t.Fatalf("late legacy schema fill replaced current binding: %v", err)
	}
	schemaRead.Close()
	read, err := f.service.AcquireRead(ctx, f.owner, stored, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if err := service.probeConnection(read.Context, stored, connection); err != nil {
		t.Fatalf("ordinary SH production probe: %v", err)
	}
	read.Close()
	aliasRequest := &dtos.CreateConnectionRequest{Type: alias.Type, Host: alias.Host, Port: alias.Port, Username: reader, Password: &password, Database: alias.Database}
	aliasResponse, _, err := service.Update(f.owner.Hex(), previous.ID.Hex(), &dtos.UpdateChatRequest{Connection: aliasRequest})
	if err != nil || aliasResponse.SourceID != source.SourceID || aliasResponse.SourceBindingRevision <= stored.SourceBindingRevision {
		t.Fatalf("approved alias changed canonical source: %v", err)
	}
	waitSourceAccessIdle(t, f)
	request := &dtos.CreateChatRequest{Connection: dtos.CreateConnectionRequest{Type: connection.Type, Host: connection.Host, Port: connection.Port, Username: reader, Password: &password, Database: connection.Database}}
	connected, _, err := service.Create(f.owner.Hex(), request)
	if err != nil || connected.SourceID != source.SourceID {
		t.Fatalf("production create did not establish ERP identity: %v", err)
	}
	t.Cleanup(func() { vectorization.DeleteChatVectors(ctx, connected.ID) })
	waitSourceAccessIdle(t, f)
	beforeNoPing := pgSessions.Load()
	created, _, err := service.CreateWithoutConnectionPing(f.owner.Hex(), request)
	if err != nil {
		t.Fatalf("production no-ping create: %v", err)
	}
	if pgSessions.Load() != beforeNoPing {
		t.Fatal("no-ping creation opened a real PostgreSQL session")
	}
	if _, exists := manager.GetConnectionInfo(created.ID); exists {
		t.Fatal("no-ping creation opened a persistent connection")
	}
	waitSourceAccessIdle(t, f)
	copied, _, err := service.Duplicate(f.owner.Hex(), created.ID, false, false, &tableName)
	if err != nil {
		t.Fatalf("production ERP copy: %v", err)
	}
	t.Cleanup(func() { vectorization.DeleteChatVectors(ctx, copied.ID) })
	waitSourceAccessIdle(t, f)
	copiedID, _ := primitive.ObjectIDFromHex(copied.ID)
	copyChat, err := f.chats.ReadCurrent(ctx, f.owner, copiedID)
	if err != nil || copied.ID == created.ID || copyChat.SourceID != source.SourceID || !copyChat.SourceBindingConfirmed {
		t.Fatalf("copy did not establish a distinct production binding: %v", err)
	}

	if copyChat.SelectedCollections != tableName || copyChat.Connection.CurrentSchema == nil || !strings.Contains(*copyChat.Connection.CurrentSchema, tableName) {
		t.Fatal("scoped duplicate did not persist selection before its first production schema refresh")
	}
	copyRead, err := f.service.AcquireRead(ctx, f.owner, copyChat, connection)
	if err != nil {
		t.Fatal(err)
	}
	copySchema, err := manager.GetSchemaManager().GetStoredSchemaInfo(copyRead.Context, copied.ID)
	if err != nil || copySchema == nil || len(copySchema.Tables) != 1 || copySchema.Tables[tableName].Name != tableName {
		t.Fatalf("first scoped duplicate schema: %v", err)
	}
	copyRead.Close()
	currentSource, err := f.repo.Get(ctx, source.SourceID)
	if err != nil || f.service.CanOperate(currentSource, f.owner) {
		t.Fatalf("copy granted operation permission: %v", err)
	}
	for _, contextual := range []bool{false, true} {
		t.Run(fmt.Sprintf("getter_rebinding_context_%v", contextual), func(t *testing.T) {
			independent := connection.Clone()
			originalPort := parsed.Port()
			if originalPort == "" {
				originalPort = "5432"
			}
			independent.Host, independent.Port = parsed.Hostname(), &originalPort
			unbound := models.NewChat(f.owner, independent, models.DefaultChatSettings())
			unbound.SelectedCollections = tableName
			if err := f.chats.Create(unbound); err != nil {
				t.Fatal(err)
			}
			initial := connectionConfig(unbound, independent)
			initial.InitialSchemaDeferred = true
			if err := manager.Connect(ctx, unbound.ID.Hex(), f.owner.Hex(), "", initial); err != nil {
				t.Fatal(err)
			}
			original, err := manager.GetConnection(unbound.ID.Hex())
			if err != nil || original.GetDB() == nil {
				t.Fatalf("independent raw interface unavailable: %v", err)
			}
			originalPool := original.GetDB()
			pause := &sourceAccessPausedPolicy{SourceAccessService: f.service, chatID: unbound.ID, barrier: newSourceAccessTerminal()}
			pause.armed.Store(true)
			manager.SetSourceAccess(pause)
			defer manager.SetSourceAccess(f.service)
			defer pause.barrier.finish()
			type getterResult struct {
				executor dbmanager.DBExecutor
				err      error
			}
			returned := make(chan getterResult, 1)
			go func() {
				if contextual {
					executor, err := manager.GetConnectionContext(ctx, unbound.ID.Hex())
					returned <- getterResult{executor, err}
				} else {
					executor, err := manager.GetConnection(unbound.ID.Hex())
					returned <- getterResult{executor, err}
				}
			}()
			waitSourceAccess(t, pause.barrier.started)
			nextBinding := *unbound
			nextBinding.Connection = connection.Clone()
			rebound, err := f.service.ChangeBinding(ctx, f.owner, unbound, &nextBinding, connection, f.chats,
				&chatBindingEffects{service: service, owner: f.owner, chatID: unbound.ID, probe: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := rebound.Wait(); err != nil {
				t.Fatal(err)
			}
			current, err := f.chats.ReadCurrent(ctx, f.owner, unbound.ID)
			if err != nil || current.SourceID != source.SourceID || !current.SourceBindingConfirmed {
				t.Fatalf("real ERP rebinding did not persist: %v", err)
			}
			permit, err := f.service.AcquireRead(ctx, f.owner, current, connection)
			if err != nil {
				t.Fatal(err)
			}
			defer permit.Close()
			published := connectionConfig(current, connection)
			published.InitialSchemaDeferred = true
			if err := manager.Connect(permit.Context, current.ID.Hex(), f.owner.Hex(), "", published); err != nil {
				t.Fatal(err)
			}
			pause.barrier.finish()
			select {
			case result := <-returned:
				if result.err == nil && (result.executor == nil || result.executor.GetDB() != originalPool) {
					t.Fatal("getter returned the newly published ERP pool under the old independent classification")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("paused getter did not return")
			}
			if err := originalPool.PingContext(ctx); err == nil {
				t.Fatal("rebinding left the old independent pool open")
			}
			if _, err := manager.GetConnection(current.ID.Hex()); !errors.Is(err, sourcegate.ErrMissing) {
				t.Fatalf("raw getter admitted ERP without permit: %v", err)
			}
			if _, err := manager.GetConnectionContext(ctx, current.ID.Hex()); err == nil {
				t.Fatal("context getter admitted ERP without permit")
			}
			restricted, err := manager.GetConnectionContext(permit.Context, current.ID.Hex())
			if err != nil || restricted.GetDB() != nil {
				t.Fatalf("correct permit exposed a raw ERP pool: %v", err)
			}
			if _, raw := restricted.(*dbmanager.PostgresWrapper); raw {
				t.Fatal("correct permit returned PostgresWrapper")
			}
			var rows []struct{ Database string }
			if err := restricted.Query("SELECT current_database() AS database", &rows); err != nil || len(rows) != 1 || rows[0].Database != "bi_agent" {
				t.Fatalf("restricted executor could not query its permitted target: %v", err)
			}
			permit.Close()
			if err := restricted.Query("SELECT current_database() AS database", &rows); err == nil {
				t.Fatal("ERP executor accepted a closed permit")
			}
		})
	}
	readerBefore := pgSessions.Load()
	request.Connection.Username = "unapproved_isolated_reader"
	if _, _, err := service.CreateWithoutConnectionPing(f.owner.Hex(), request); !errors.Is(err, ErrSourceUnresolved) {
		t.Fatalf("reader change escaped approved ERP identity: %v", err)
	}
	if pgSessions.Load() != readerBefore {
		t.Fatal("unapproved reader reached PostgreSQL")
	}
	request.Connection.Username, request.Connection.Host = reader, "unapproved-endpoint.invalid"
	if _, _, err := service.Update(f.owner.Hex(), copied.ID, &dtos.UpdateChatRequest{Connection: &request.Connection}); !errors.Is(err, ErrSourceUnresolved) {
		t.Fatalf("copied ERP endpoint escaped lineage: %v", err)
	}
	if pgSessions.Load() != readerBefore {
		t.Fatal("unresolved endpoint reached PostgreSQL")
	}
	copyChat, err = f.chats.ReadCurrent(ctx, f.owner, copiedID)
	if err != nil || !copyChat.ERPAssociated || copyChat.SourceBindingConfirmed {
		t.Fatalf("lost sticky unresolved copy lineage: %v", err)
	}
	connectedID, err := primitive.ObjectIDFromHex(connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelPrevious, err := f.chats.ReadCurrent(ctx, f.owner, connectedID)
	if err != nil {
		t.Fatal(err)
	}
	cancelNext := *cancelPrevious
	cancelSQL, err := control.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancelSQL.Rollback() })
	if _, err := cancelSQL.ExecContext(ctx, `LOCK TABLE public."`+tableName+`" IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	cancelContext, cancelBinding := context.WithCancel(ctx)
	defer cancelBinding()
	armCancelKBWrite.Store(true)
	change, err = f.service.ChangeBinding(cancelContext, f.owner, cancelPrevious, &cancelNext, connection, f.chats, &chatBindingEffects{service: service, owner: f.owner, chatID: connectedID, probe: true, refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	// Metadata-only refresh cannot be stalled via a table lock; the KB write
	// terminal keeps the binding genuinely in flight at the moment of cancel.
	waitSourceAccess(t, cancelKBBarrier.started)
	if err := (&sourceAccessEffects{fixture: f}).checkExclusion(); err != nil {
		t.Fatal(err)
	}
	cancelBinding()
	cancelKBBarrier.finish()
	if err := change.Wait(); err == nil {
		t.Fatal("cancelled in-flight binding reported success")
	}
	waitSourceAccessSQL(t, control, source.SourceID, false)
	cancelledSource, err := f.repo.Get(ctx, source.SourceID)
	if err != nil || cancelledSource.Phase != models.ERPSourceRecovering || cancelledSource.ActiveOperation == nil {
		t.Fatalf("cancelled SQL lost its durable recovery veto: %v", err)
	}
	cancelCurrent, err := f.chats.ReadCurrent(ctx, f.owner, connectedID)
	if err != nil {
		t.Fatal(err)
	}
	if read, err := f.service.AcquireRead(ctx, f.owner, cancelCurrent, connection); err == nil {
		read.Close()
		t.Fatal("cancelled SQL binding remained executable")
	}
	lease, err := f.gate.Acquire(ctx, sourceTarget(cancelledSource), sourcegate.Exclusive, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
	if err != nil {
		t.Fatalf("actual SQL cleanup left a live flock reference: %v", err)
	}
	lease.Close()
	if err := cancelSQL.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func waitSourceAccessIdle(t *testing.T, fixture *sourceAccessFixture) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		source, err := fixture.repo.Get(context.Background(), fixture.sources[0].SourceID)
		if err != nil {
			t.Fatal(err)
		}
		if source.Phase == models.ERPSourceIdle && source.ActiveOperation == nil {
			lease, err := fixture.gate.Acquire(context.Background(), sourceTarget(source), sourcegate.Exclusive, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
			if err == nil {
				lease.Close()
				return
			}
			if !errors.Is(err, sourcegate.ErrBusy) {
				t.Fatal(err)
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("production binding did not reach its joined terminal state")
		case <-ticker.C:
		}
	}
}

func waitSourceAccessSQL(t *testing.T, control *sql.DB, sourceID string, blocked bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var present bool
		err := control.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND application_name LIKE $1
			AND state = 'active' AND (NOT $2 OR wait_event_type = 'Lock')
		)`, "yanfa:"+sourceID+":%", blocked).Scan(&present)
		if err != nil {
			t.Fatal(err)
		}
		if present == blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("actual PostgreSQL source activity did not reach blocked=%v", blocked)
		case <-ticker.C:
		}
	}
}

// The metadata-only refresh never samples locked tables, so no source query
// blocks on a table lock; a pooled source-identity session is the durable
// proof that the refresh reached PostgreSQL.
func waitSourceAccessSession(t *testing.T, control *sql.DB, sourceID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var present bool
		err := control.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND application_name LIKE $1
		)`, "yanfa:"+sourceID+":%").Scan(&present)
		if err != nil {
			t.Fatal(err)
		}
		if present {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no PostgreSQL source session for %s", sourceID)
		case <-ticker.C:
		}
	}
}

func sourceAccessPGConnection(t *testing.T, rawURL string) models.Connection {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User == nil || parsed.Hostname() == "" || parsed.Path == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("an explicit isolated PostgreSQL URL is required")
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	username := parsed.User.Username()
	password, _ := parsed.User.Password()
	return models.Connection{Type: constants.DatabaseTypePostgreSQL, Host: parsed.Hostname(), Port: &port, Username: &username, Password: &password, Database: strings.TrimPrefix(parsed.Path, "/")}
}

func sourceAccessPGProxy(t *testing.T, target models.Connection) (models.Connection, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	sessions := &atomic.Int32{}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			sessions.Add(1)
			go func() {
				upstream, err := net.Dial("tcp", net.JoinHostPort(target.Host, *target.Port))
				if err != nil {
					client.Close()
					return
				}
				finished := make(chan struct{})
				go func() { io.Copy(upstream, client); upstream.Close(); client.Close(); close(finished) }()
				io.Copy(client, upstream)
				client.Close()
				upstream.Close()
				<-finished
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	proxied := target.Clone()
	proxied.Host, proxied.Port = host, &port
	return proxied, sessions
}

func TestSourceAccessPostgresConninfoTargets(t *testing.T) {
	aURL, bURL := os.Getenv("SOURCE_ACCESS_TEST_PG_URL"), os.Getenv("SOURCE_ACCESS_TEST_PG_B_URL")
	if aURL == "" || bURL == "" {
		t.Skip("conninfo target isolation unverified: two explicit isolated PostgreSQL server endpoints are required")
	}
	a, b := sourceAccessPGConnection(t, aURL), sourceAccessPGConnection(t, bURL)
	if net.JoinHostPort(a.Host, *a.Port) == net.JoinHostPort(b.Host, *b.Port) {
		t.Fatal("conninfo isolation requires two distinct PostgreSQL server endpoints")
	}
	a, aSessions := sourceAccessPGProxy(t, a)
	b, bSessions := sourceAccessPGProxy(t, b)
	manager, err := dbmanager.NewManager(nil, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var certificateRequests, sshRequests atomic.Int32
	certificates := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		certificateRequests.Add(1)
		http.Error(w, "invalid mode must not fetch certificates", http.StatusBadRequest)
	}))
	t.Cleanup(certificates.Close)
	ssh, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ssh.Close() })
	go func() {
		for {
			client, err := ssh.Accept()
			if err != nil {
				return
			}
			sshRequests.Add(1)
			client.Close()
		}
	}()
	sshHost, sshPort, _ := net.SplitHostPort(ssh.Addr().String())
	sshUser, sshPassword, sshAuth := "isolated-only", "isolated-only", "password"
	invalidModes := []string{
		fmt.Sprintf("disable host=%s port=%s dbname=%s user=%s password=%s", b.Host, *b.Port, b.Database, *b.Username, *b.Password),
		"require sslmode=disable", "disable\nhost=127.0.0.1", "disable ", "allow", "prefer", "",
	}
	for _, mode := range invalidModes {
		for _, useSSH := range []bool{false, true} {
			bad := connectionConfig(models.NewChat(primitive.NewObjectID(), a, models.DefaultChatSettings()), a)
			bad.UseSSL, bad.SSLMode = true, &mode
			bad.SSLCertURL, bad.SSLKeyURL, bad.SSLRootCertURL = &certificates.URL, &certificates.URL, &certificates.URL
			bad.SSHEnabled, bad.SSHHost, bad.SSHPort, bad.SSHUsername = useSSH, &sshHost, &sshPort, &sshUser
			bad.SSHPassword, bad.SSHAuthMethod = &sshPassword, &sshAuth
			if err := manager.TestConnection(ctx, &bad); err == nil || !strings.Contains(err.Error(), "invalid PostgreSQL SSL mode") {
				t.Fatalf("invalid mode did not fail at the connection entry: %v", err)
			}
			bad.InitialSchemaDeferred = true
			if err := manager.Connect(ctx, bad.ChatID, bad.SourceChat.UserID.Hex(), "", bad); err == nil || !strings.Contains(err.Error(), "invalid PostgreSQL SSL mode") {
				t.Fatalf("persistent connection accepted invalid mode: %v", err)
			}
			if aSessions.Load() != 0 || bSessions.Load() != 0 || sshRequests.Load() != 0 || certificateRequests.Load() != 0 {
				t.Fatal("invalid SSL mode reached PG, SSH or certificate IO")
			}
		}
	}
	invalidTarget := connectionConfig(models.NewChat(primitive.NewObjectID(), a, models.DefaultChatSettings()), a)
	invalidTarget.Database = "declared_missing\x00database\x00" + b.Database
	require := "require"
	invalidTarget.UseSSL, invalidTarget.SSLMode = true, &require
	invalidTarget.SSLCertURL, invalidTarget.SSLKeyURL, invalidTarget.SSLRootCertURL = &certificates.URL, &certificates.URL, &certificates.URL
	invalidTarget.SSHEnabled, invalidTarget.SSHHost, invalidTarget.SSHPort, invalidTarget.SSHUsername = true, &sshHost, &sshPort, &sshUser
	invalidTarget.SSHPassword, invalidTarget.SSHAuthMethod = &sshPassword, &sshAuth
	if err := manager.TestConnection(ctx, &invalidTarget); err == nil || !strings.Contains(err.Error(), "invalid PostgreSQL connection parameter") {
		t.Fatalf("startup delimiter injection reached probe: %v", err)
	}
	invalidTarget.InitialSchemaDeferred = true
	if err := manager.Connect(ctx, invalidTarget.ChatID, invalidTarget.SourceChat.UserID.Hex(), "", invalidTarget); err == nil || !strings.Contains(err.Error(), "invalid PostgreSQL connection parameter") {
		t.Fatalf("startup delimiter injection reached persistent connection: %v", err)
	}
	if aSessions.Load() != 0 || bSessions.Load() != 0 || sshRequests.Load() != 0 || certificateRequests.Load() != 0 {
		t.Fatal("startup delimiter injection reached PG, SSH or certificate IO")
	}
	for _, target := range []models.Connection{a, b} {
		mode := "disable"
		cfg := connectionConfig(models.NewChat(primitive.NewObjectID(), target, models.DefaultChatSettings()), target)
		cfg.UseSSL, cfg.SSLMode = true, &mode
		cfg.ApplicationName = "t37-valid-mode"
		sourceAccessAssertPGIdentity(t, ctx, manager, cfg)
	}
}

func sourceAccessAssertPGIdentity(t *testing.T, ctx context.Context, manager *dbmanager.Manager, cfg dbmanager.ConnectionConfig) {
	t.Helper()
	cfg.InitialSchemaDeferred = true
	if err := manager.Connect(ctx, cfg.ChatID, cfg.SourceChat.UserID.Hex(), "", cfg); err != nil {
		t.Fatalf("valid conninfo did not authenticate at its original target: %v", err)
	}
	defer func() {
		if err := manager.Disconnect(cfg.ChatID, cfg.SourceChat.UserID.Hex(), false); err != nil {
			t.Fatal(err)
		}
	}()
	executor, err := manager.GetConnection(cfg.ChatID)
	if err != nil || executor.GetDB() == nil {
		t.Fatalf("authenticated independent executor unavailable: %v", err)
	}
	var database, user, application string
	err = executor.GetDB().QueryRowContext(ctx, "SELECT current_database(), current_user, current_setting('application_name')").Scan(&database, &user, &application)
	if err != nil || database != cfg.Database || user != *cfg.Username || application != cfg.ApplicationName {
		t.Fatalf("conninfo values changed the authenticated identity or application name: %v", err)
	}
}

func TestSourceAccessPostgresConninfoAuthentication(t *testing.T) {
	aURL := os.Getenv("SOURCE_ACCESS_TEST_AUTH_PG_URL")
	if aURL == "" {
		t.Skip("conninfo authentication unverified: an explicit isolated password-auth PostgreSQL URL is required")
	}
	a := sourceAccessPGConnection(t, aURL)
	for _, value := range []string{*a.Username, *a.Password} {
		for _, required := range []string{"'", "\\", " "} {
			if !strings.Contains(value, required) {
				t.Fatal("password-auth fixture role and password must both contain a quote, backslash and whitespace")
			}
		}
	}
	if !strings.Contains(*a.Password, "=") {
		t.Fatal("password-auth fixture password must contain conninfo-like text")
	}
	manager, err := dbmanager.NewManager(nil, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Requiring SSL in the environment must not override either explicit disable path.
	t.Setenv("PGSSLMODE", "require")
	for _, useSSL := range []bool{false, true} {
		mode := "disable"
		cfg := connectionConfig(models.NewChat(primitive.NewObjectID(), a, models.DefaultChatSettings()), a)
		cfg.UseSSL, cfg.SSLMode = useSSL, &mode
		cfg.ApplicationName = "t37 ' \\ application host=elsewhere user=elsewhere"
		sourceAccessAssertPGIdentity(t, ctx, manager, cfg)
	}
	wrong := connectionConfig(models.NewChat(primitive.NewObjectID(), a, models.DefaultChatSettings()), a)
	wrongPassword := *a.Password + "-wrong"
	wrong.Password = &wrongPassword
	var authentication *pq.Error
	if err := manager.TestConnection(ctx, &wrong); !errors.As(err, &authentication) || authentication.Code != "28P01" {
		t.Fatal("special-character fixture did not prove real password authentication; trust authentication is insufficient")
	}
}

func sourceAccessPGPoolConfig(connection models.Connection) dbmanager.ConnectionConfig {
	chat := models.NewChat(primitive.NewObjectID(), connection.Clone(), models.DefaultChatSettings())
	cfg := connectionConfig(chat, chat.Connection)
	cfg.InitialSchemaDeferred = true
	cfg.ApplicationName = "t37-tls-pool"
	return cfg
}

func sourceAccessConnectPGPool(t *testing.T, ctx context.Context, manager *dbmanager.Manager, cfg dbmanager.ConnectionConfig) {
	t.Helper()
	if err := manager.Connect(ctx, cfg.ChatID, cfg.SourceChat.UserID.Hex(), "", cfg); err != nil {
		t.Fatalf("production Manager could not open the requested PostgreSQL transport: %v", err)
	}
}

func sourceAccessAssertPGSessionTLS(t *testing.T, ctx context.Context, manager *dbmanager.Manager, cfg dbmanager.ConnectionConfig, wantTLS bool) int {
	t.Helper()
	executor, err := manager.GetConnection(cfg.ChatID)
	if err != nil {
		t.Fatalf("independent PostgreSQL connection unavailable: %v", err)
	}
	if executor.GetDB() == nil {
		t.Fatal("independent PostgreSQL connection has no SQL pool")
	}
	var database, user string
	var pid int
	var actualTLS bool
	err = executor.GetDB().QueryRowContext(ctx, `SELECT current_database(), current_user, pid, ssl
		FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&database, &user, &pid, &actualTLS)
	if err != nil {
		t.Fatalf("could not observe the actual PostgreSQL session transport: %v", err)
	}
	if database != cfg.Database || user != *cfg.Username || actualTLS != wantTLS {
		t.Fatalf("PostgreSQL session identity or TLS differs from the request: ssl=%t, want=%t", actualTLS, wantTLS)
	}
	t.Logf("observed PostgreSQL backend_pid=%d ssl=%t", pid, actualTLS)
	return pid
}

func TestSourceAccessPostgresPoolTLSIdentityWithoutTLS(t *testing.T) {
	rawURL := os.Getenv("SOURCE_ACCESS_TEST_PG_URL")
	if rawURL == "" {
		t.Skip("active-pool TLS upgrade unverified: SOURCE_ACCESS_TEST_PG_URL must identify an isolated PostgreSQL server without TLS")
	}
	base := sourceAccessPGConnection(t, rawURL)
	for _, databaseType := range []string{constants.DatabaseTypePostgreSQL, constants.DatabaseTypeYugabyteDB, constants.DatabaseTypeTimescaleDB} {
		t.Run(databaseType, func(t *testing.T) {
			manager, err := dbmanager.NewManager(nil, "0123456789abcdef0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			target := base.Clone()
			target.Type = databaseType
			plain := sourceAccessPGPoolConfig(target)
			sourceAccessConnectPGPool(t, ctx, manager, plain)
			plainPID := sourceAccessAssertPGSessionTLS(t, ctx, manager, plain, false)
			// Keep the original chat and pool active throughout every stronger request.
			for _, mode := range []string{"require", "verify-ca", "verify-full"} {
				t.Run(mode, func(t *testing.T) {
					stronger := target.Clone()
					stronger.UseSSL, stronger.SSLMode = true, &mode
					cfg := sourceAccessPGPoolConfig(stronger)
					if err := manager.Connect(ctx, cfg.ChatID, cfg.SourceChat.UserID.Hex(), "", cfg); !errors.Is(err, pq.ErrSSLNotSupported) {
						t.Fatalf("stronger TLS request did not reach the real non-TLS server: %v", err)
					}
					sourceAccessAssertPGSessionTLS(t, ctx, manager, plain, false)
				})
			}
			disable, ignoredURL := "disable", "http://127.0.0.1:1/unused-certificate.pem"
			equivalent := target.Clone()
			equivalent.UseSSL, equivalent.SSLMode = true, &disable
			equivalent.SSLCertURL, equivalent.SSLKeyURL, equivalent.SSLRootCertURL = &ignoredURL, &ignoredURL, &ignoredURL
			alias := sourceAccessPGPoolConfig(equivalent)
			sourceAccessConnectPGPool(t, ctx, manager, alias)
			if sourceAccessAssertPGSessionTLS(t, ctx, manager, alias, false) != plainPID {
				t.Fatal("equivalent disabled TLS requests did not share the original PostgreSQL session")
			}
			if err := manager.Disconnect(alias.ChatID, alias.SourceChat.UserID.Hex(), false); err != nil {
				t.Fatal(err)
			}
			sourceAccessAssertPGSessionTLS(t, ctx, manager, plain, false)
		})
	}
}

func TestSourceAccessPostgresPoolTLSIdentityWithTLS(t *testing.T) {
	rawURL := os.Getenv("SOURCE_ACCESS_TEST_TLS_PG_URL")
	caPath, wrongCAPath := os.Getenv("SOURCE_ACCESS_TEST_TLS_CA_PATH"), os.Getenv("SOURCE_ACCESS_TEST_TLS_WRONG_CA_PATH")
	if rawURL == "" || caPath == "" || wrongCAPath == "" {
		t.Skip("verified TLS pool isolation unverified: SOURCE_ACCESS_TEST_TLS_PG_URL, SOURCE_ACCESS_TEST_TLS_CA_PATH and SOURCE_ACCESS_TEST_TLS_WRONG_CA_PATH are required")
	}
	// These inputs contain public synthetic CA certificates only, never private keys.
	ca, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	wrongCA, err := os.ReadFile(wrongCAPath)
	if err != nil {
		t.Fatal(err)
	}
	certificates := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		switch r.URL.Path {
		case "/ca.pem":
			w.Write(ca)
		case "/wrong-ca.pem":
			w.Write(wrongCA)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(certificates.Close)
	rootURL, wrongRootURL := certificates.URL+"/ca.pem", certificates.URL+"/wrong-ca.pem"
	base := sourceAccessPGConnection(t, rawURL)
	for _, databaseType := range []string{constants.DatabaseTypePostgreSQL, constants.DatabaseTypeYugabyteDB, constants.DatabaseTypeTimescaleDB} {
		t.Run(databaseType, func(t *testing.T) {
			manager, err := dbmanager.NewManager(nil, "0123456789abcdef0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			target := base.Clone()
			target.Type = databaseType
			plain := sourceAccessPGPoolConfig(target)
			sourceAccessConnectPGPool(t, ctx, manager, plain)
			plainPID := sourceAccessAssertPGSessionTLS(t, ctx, manager, plain, false)
			required := target.Clone()
			required.UseSSL = true // A nil mode is the driver's effective require default.
			requireConfig := sourceAccessPGPoolConfig(required)
			sourceAccessConnectPGPool(t, ctx, manager, requireConfig)
			requirePID := sourceAccessAssertPGSessionTLS(t, ctx, manager, requireConfig, true)
			if requirePID == plainPID {
				t.Fatal("require reused the active plaintext PostgreSQL session")
			}
			mode, empty := "require", ""
			equivalent := required.Clone()
			equivalent.SSLMode = &mode
			equivalent.SSLCertURL, equivalent.SSLKeyURL, equivalent.SSLRootCertURL = &empty, &empty, &empty
			requireAlias := sourceAccessPGPoolConfig(equivalent)
			sourceAccessConnectPGPool(t, ctx, manager, requireAlias)
			if sourceAccessAssertPGSessionTLS(t, ctx, manager, requireAlias, true) != requirePID {
				t.Fatal("nil/default and explicit require with empty certificate URLs did not share the effective TLS session")
			}
			if err := manager.Disconnect(requireAlias.ChatID, requireAlias.SourceChat.UserID.Hex(), false); err != nil {
				t.Fatal(err)
			}
			sourceAccessAssertPGSessionTLS(t, ctx, manager, requireConfig, true)
			verifyCA, verifyFull := "verify-ca", "verify-full"
			verified := required.Clone()
			// Server authentication needs only the root URL; client cert/key stay nil.
			verified.SSLMode, verified.SSLRootCertURL = &verifyCA, &rootURL
			caConfig := sourceAccessPGPoolConfig(verified)
			sourceAccessConnectPGPool(t, ctx, manager, caConfig)
			caPID := sourceAccessAssertPGSessionTLS(t, ctx, manager, caConfig, true)
			verified.SSLMode = &verifyFull
			fullConfig := sourceAccessPGPoolConfig(verified)
			sourceAccessConnectPGPool(t, ctx, manager, fullConfig)
			fullPID := sourceAccessAssertPGSessionTLS(t, ctx, manager, fullConfig, true)
			if fullPID == caPID || fullPID == requirePID {
				t.Fatal("verify-full reused a session opened with weaker verification")
			}
			fullAlias := sourceAccessPGPoolConfig(verified)
			sourceAccessConnectPGPool(t, ctx, manager, fullAlias)
			if sourceAccessAssertPGSessionTLS(t, ctx, manager, fullAlias, true) != fullPID {
				t.Fatal("equal verified TLS values did not preserve actual session reuse")
			}
			if err := manager.Disconnect(fullAlias.ChatID, fullAlias.SourceChat.UserID.Hex(), false); err != nil {
				t.Fatal(err)
			}
			probe := sourceAccessPGPoolConfig(verified)
			if err := manager.TestConnection(ctx, &probe); err != nil {
				t.Fatalf("root-only verify-full probe rejected the correct public CA: %v", err)
			}
			probe.SSLRootCertURL = &wrongRootURL
			var untrusted x509.UnknownAuthorityError
			if err := manager.TestConnection(ctx, &probe); !errors.As(err, &untrusted) {
				t.Fatalf("root-only verify-full probe did not reject the wrong public CA: %v", err)
			}
			for _, mode := range []string{"require", "verify-ca", "verify-full"} {
				t.Run("wrong_ca_"+mode, func(t *testing.T) {
					wrong := verified.Clone()
					wrong.SSLMode, wrong.SSLRootCertURL = &mode, &wrongRootURL
					cfg := sourceAccessPGPoolConfig(wrong)
					var untrusted x509.UnknownAuthorityError
					if err := manager.Connect(ctx, cfg.ChatID, cfg.SourceChat.UserID.Hex(), "", cfg); !errors.As(err, &untrusted) {
						t.Fatalf("wrong public CA did not fail real certificate verification while the valid pool remained active: %v", err)
					}
					sourceAccessAssertPGSessionTLS(t, ctx, manager, fullConfig, true)
					sourceAccessAssertPGSessionTLS(t, ctx, manager, plain, false)
				})
			}
			sourceAccessAssertPGSessionTLS(t, ctx, manager, requireConfig, true)
			sourceAccessAssertPGSessionTLS(t, ctx, manager, caConfig, true)
		})
	}
}

// Observe the real SDK terminal without replacing model behavior or KB storage.
type sourceAccessObservedModel struct {
	llm.Client
	done chan error
}

func (m *sourceAccessObservedModel) GenerateRawJSON(ctx context.Context, systemPrompt, userMessage string, modelID ...string) (string, error) {
	response, err := m.Client.GenerateRawJSON(ctx, systemPrompt, userMessage, modelID...)
	m.done <- err
	return response, err
}

func TestSourceAccessIndependentFirstConnectOptionalEnhancement(t *testing.T) {
	pgURL, redisAddress := os.Getenv("SOURCE_ACCESS_TEST_PG_URL"), os.Getenv("SOURCE_ACCESS_TEST_REDIS_ADDR")
	if pgURL == "" || redisAddress == "" {
		t.Skip("independent first connect unverified: isolated PostgreSQL and Redis inputs are required")
	}
	f := newSourceAccessFixture(t, 0)
	connection := sourceAccessPGConnection(t, pgURL)
	control, err := sql.Open("postgres", pgURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	tableName := "source_access_optional_" + primitive.NewObjectID().Hex()
	if _, err := control.Exec(`CREATE TABLE public."` + tableName + `" (id integer PRIMARY KEY, label text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Exec(`DROP TABLE public."` + tableName + `"`) })
	if _, err := control.Exec(`INSERT INTO public."` + tableName + `" VALUES (1, 'usable without model')`); err != nil {
		t.Fatal(err)
	}
	redisClient := goredis.NewClient(&goredis.Options{Addr: redisAddress})
	t.Cleanup(func() { redisClient.Close() })
	redisStore := redisrepo.NewRedisRepositories(redisClient)
	mongoClient := &mongodb.MongoDBClient{Client: f.client, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}
	f.chats = repositories.NewChatRepository(mongoClient, redisStore)
	f.service.chats = f.chats
	kb := repositories.NewKnowledgeBaseRepository(mongoClient)
	oldKey, oldDataKey := config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey
	config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey = "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"
	t.Cleanup(func() { config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey = oldKey, oldDataKey })
	for _, mode := range []string{"no_model", "model_failure", "model_blocked"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			manager, err := dbmanager.NewManager(redisStore, "0123456789abcdef0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			barrier := newSourceAccessTerminal()
			defer barrier.finish()
			var model llm.Client
			var observed *sourceAccessObservedModel
			if mode != "no_model" {
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if mode == "model_blocked" {
						barrier.enter()
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":{"message":"isolated model unavailable","type":"invalid_request_error"}}`)
				}))
				defer endpoint.Close()
				// Unblock the real HTTP handler before its server cleanup on failure.
				defer barrier.finish()
				client, err := llm.NewOpenAIClient(llm.Config{APIKey: "isolated-only", BaseURL: endpoint.URL + "/v1", Model: "isolated-optional"})
				if err != nil {
					t.Fatal(err)
				}
				observed = &sourceAccessObservedModel{Client: client, done: make(chan error, 1)}
				model = observed
			}
			chat := models.NewChat(f.owner, connection.Clone(), models.DefaultChatSettings())
			chat.SelectedCollections = tableName
			if err := f.chats.Create(chat); err != nil {
				t.Fatal(err)
			}
			knowledge := models.NewKnowledgeBase(chat.ID)
			knowledge.TableDescriptions = []models.TableDescription{{TableName: tableName, Description: "preserved optional knowledge"}}
			if err := kb.Upsert(ctx, knowledge); err != nil {
				t.Fatal(err)
			}
			service := NewChatService(f.chats, manager, model, llm.NewManager(), redisStore, nil, nil, kb, nil, nil, f.service).(*chatService)
			manager.SetStreamHandler(service)
			type connectResult struct {
				status uint32
				err    error
			}
			returned := make(chan connectResult, 1)
			go func() {
				status, err := service.ConnectDB(ctx, f.owner.Hex(), chat.ID.Hex(), "")
				returned <- connectResult{status, err}
			}()
			if mode == "model_blocked" {
				waitSourceAccess(t, barrier.started)
			}
			select {
			case result := <-returned:
				if result.err != nil || result.status != http.StatusOK {
					t.Fatalf("optional enhancement broke first ConnectDB: status=%d error=%v", result.status, result.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("first ConnectDB waited for optional model completion")
			}
			if !manager.IsConnected(chat.ID.Hex()) {
				t.Fatal("successful first ConnectDB disconnected its PostgreSQL connection")
			}
			stored, err := f.chats.ReadCurrent(ctx, f.owner, chat.ID)
			if err != nil || stored.Connection.CurrentSchema == nil || !strings.Contains(*stored.Connection.CurrentSchema, tableName) {
				t.Fatalf("first ConnectDB did not persist the required schema: %v", err)
			}
			executor, err := manager.GetConnection(chat.ID.Hex())
			if err != nil || executor.GetDB() == nil {
				t.Fatalf("independent database interface unavailable: %v", err)
			}
			var label string
			if err := executor.GetDB().QueryRowContext(ctx, `SELECT label FROM public."`+tableName+`" WHERE id = 1`).Scan(&label); err != nil || label != "usable without model" {
				t.Fatalf("first connection could not read its real PostgreSQL data: %v", err)
			}
			if observed != nil {
				if mode == "model_blocked" {
					select {
					case <-observed.done:
						t.Fatal("model terminal finished while its real HTTP reply was blocked")
					default:
					}
				}
				barrier.finish()
				select {
				case err := <-observed.done:
					if err == nil {
						t.Fatal("failure fixture did not return a real SDK error")
					}
				case <-ctx.Done():
					t.Fatal("real model error was not observed")
				}
				if !manager.IsConnected(chat.ID.Hex()) || executor.GetDB().PingContext(ctx) != nil {
					t.Fatal("optional model failure disconnected the usable PostgreSQL pool")
				}
			}
			preserved, err := kb.FindByChatID(ctx, chat.ID)
			if err != nil || preserved == nil || preserved.GetTableDescription(tableName) == nil || preserved.GetTableDescription(tableName).Description != "preserved optional knowledge" {
				t.Fatalf("optional enhancement discarded the production KB: %v", err)
			}
		})
	}
}
