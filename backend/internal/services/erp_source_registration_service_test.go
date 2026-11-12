package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/sourcegate"
)

type registrationFaultStore struct {
	repositories.ERPSourceRepository
	repositories.ERPSourceRegistrationStore
	point    string
	failRead bool
}

var errRegistrationInterrupted = errors.New("synthetic registration interruption")

func (r *registrationFaultStore) Create(ctx context.Context, s *models.ERPSource) error {
	err := r.ERPSourceRepository.Create(ctx, s)
	if err == nil && r.point == "create" {
		r.failRead = true
	}
	return err
}
func (r *registrationFaultStore) FindRegistrationSource(ctx context.Context, e string, a []string) (*models.ERPSource, error) {
	if r.failRead {
		r.failRead = false
		r.point = ""
		return nil, errRegistrationInterrupted
	}
	return r.ERPSourceRegistrationStore.FindRegistrationSource(ctx, e, a)
}
func (r *registrationFaultStore) Get(ctx context.Context, id string) (*models.ERPSource, error) {
	if r.failRead {
		r.failRead = false
		r.point = ""
		return nil, errRegistrationInterrupted
	}
	return r.ERPSourceRepository.Get(ctx, id)
}
func (r *registrationFaultStore) PutBindingCAS(ctx context.Context, id string, rev int64, old *models.ERPChatBinding, next models.ERPChatBinding) error {
	err := r.ERPSourceRepository.PutBindingCAS(ctx, id, rev, old, next)
	if err == nil && r.point == "put" {
		r.failRead = true
	}
	return err
}
func (r *registrationFaultStore) ConfirmBindingCAS(ctx context.Context, id string, rev int64, next models.ERPChatBinding) error {
	err := r.ERPSourceRepository.ConfirmBindingCAS(ctx, id, rev, next)
	if err == nil && r.point == "confirm" {
		r.failRead = true
	}
	return err
}
func (r *registrationFaultStore) UpdateConfigCAS(ctx context.Context, id string, rev int64, next *models.ERPSource) error {
	err := r.ERPSourceRepository.UpdateConfigCAS(ctx, id, rev, next)
	if err == nil && r.point == "config" {
		r.failRead = true
	}
	return err
}
func (r *registrationFaultStore) UpdateStateCAS(ctx context.Context, id string, rev int64, op string, next repositories.ERPSourceState) error {
	err := r.ERPSourceRepository.UpdateStateCAS(ctx, id, rev, op, next)
	if err == nil && r.point == "state" {
		r.failRead = true
	}
	return err
}
func (r *registrationFaultStore) RegistrationCAS(ctx context.Context, id string, old, next *models.ERPRegistrationReceipt) error {
	err := r.ERPSourceRegistrationStore.RegistrationCAS(ctx, id, old, next)
	if err == nil && r.point == "receipt" {
		r.failRead = true
	}
	return err
}

type registrationChatFault struct {
	repositories.ChatRepository
	point    string
	failRead bool
}

func (r *registrationChatFault) ReadCurrent(ctx context.Context, o, id primitive.ObjectID) (*models.Chat, error) {
	if r.failRead {
		r.failRead = false
		r.point = ""
		return nil, errRegistrationInterrupted
	}
	return r.ChatRepository.ReadCurrent(ctx, o, id)
}
func (r *registrationChatFault) MarkPendingCAS(ctx context.Context, a, b *models.Chat) error {
	err := r.ChatRepository.MarkPendingCAS(ctx, a, b)
	if err == nil && r.point == "mark" {
		r.failRead = true
	}
	return err
}
func (r *registrationChatFault) CommitBindingCAS(ctx context.Context, a, b *models.Chat) error {
	err := r.ChatRepository.CommitBindingCAS(ctx, a, b)
	if err == nil && r.point == "commit" {
		r.failRead = true
	}
	return err
}

type registrationWorld struct {
	f         *sourceAccessFixture
	pg        *erpSyncPGFixture
	cfg       config.ERPSyncConfig
	req       ERPRegistrationViewRequest
	registrar *ERPSourceRegistrationService
	repo      *registrationFaultStore
	chats     *registrationChatFault
}

func newRegistrationWorld(t *testing.T) *registrationWorld {
	t.Helper()
	if os.Getenv("ASSISTANT_TEST_MONGO_URI") == "" || os.Getenv("ERP_SYNC_TEST_PG_URL") == "" {
		t.Fatal("manager must supply isolated Mongo/PG inputs")
	}
	f := newSourceAccessFixture(t, 0)
	pg := newERPSyncPGFixture(t)
	directory := fmt.Sprintf("/run/user/%d/yanfa-bi-chat/source-gates", os.Getuid())
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })
	reader, port := pg.runtime.ReaderRoleByRef["isolated-reader-ref"], "18545"
	connection := models.Connection{Type: "postgresql", Host: "127.0.0.1", Port: &port, Username: &reader, Database: "bi_agent"}
	chat := models.NewChat(f.owner, connection, models.DefaultChatSettings())
	if f.chats.Create(chat) != nil {
		t.Fatal("existing chat fixture")
	}
	cfg := config.ERPSyncConfig{InstanceID: "synthetic-t40-registration", DeploymentUID: uint32(os.Getuid()), LockDirectory: directory, Sync: config.ERPSyncRuntime{Sources: map[string]config.ERPSourceRuntime{}}}
	home, _ := os.UserHomeDir()
	scratch, err := os.MkdirTemp(home, ".t40-registration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch) })
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(scratch, "before.json")
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("publication fixture")
	}
	runtime := pg.runtime
	runtime.ManifestRef = "t45:f1474d4d6ecbd6a9e9c18b85d7a7d838aca948d3:077c3be10a76bc9251f68660db1e2e55b08807c077ddaaf615547d4d0ce3d282"
	runtime.PrivateEnvironment = config.SecretFileReference{Reference: "synthetic-writer-private", UID: uint32(os.Getuid()), Mode: 0600}
	runtime.SingleBackendApprovalRef = "synthetic-stop-window"
	runtime.RuntimeMountNamespace, _ = os.Readlink("/proc/self/ns/mnt")
	runtime.RuntimePIDNamespace, _ = os.Readlink("/proc/self/ns/pid")
	source := models.ERPSource{EnvironmentRef: "synthetic-owned-18545", PhysicalDatabase: "bi_agent", ApprovedEndpointAliases: []string{EndpointFingerprint(connection)}, ApprovedConnectionFingerprints: []string{ConnectionFingerprint(connection)}, ReaderRoleRefs: []string{"isolated-reader-ref"}, WriterRoleRef: "isolated-writer-ref", ComponentManifestRef: runtime.ManifestRef, SecretInjectionRef: runtime.PrivateEnvironment.Reference, ViewAccountIDs: []primitive.ObjectID{f.owner}, ShopScope: []string{"1", "2", "3", "4", "5", "6", "7"}, OrderSourceByShop: pg.mapping, ApprovalRefs: map[string]string{"Q15-02": "synthetic-view-only"}}
	repo := &registrationFaultStore{ERPSourceRepository: f.repo, ERPSourceRegistrationStore: f.repo.(repositories.ERPSourceRegistrationStore)}
	chats := &registrationChatFault{ChatRepository: f.chats}
	access, err := NewSourceAccessService(repo, gate, cfg, chats)
	if err != nil {
		t.Fatal(err)
	}
	f.service = access
	f.gate = gate
	registrar, err := NewERPSourceRegistrationService(access, repositories.NewDataSyncRepository(f.database), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &registrationWorld{f: f, pg: pg, cfg: cfg, repo: repo, chats: chats, registrar: registrar, req: ERPRegistrationViewRequest{RequestID: uuid.NewString(), PublicConfigPath: path, PublicConfigBeforeSHA256: syncDigest(raw), OwnerID: f.owner, ChatID: chat.ID, Source: source, Runtime: runtime, OperatorApprovalRef: "synthetic-only", LegacyWriterDisabledRef: "synthetic-no-legacy-writer"}}
}
func TestERPRegistrationResumesActualBindingWindows(t *testing.T) {
	for _, point := range []string{"create", "mark", "put", "commit", "confirm", "receipt"} {
		t.Run(point, func(t *testing.T) {
			w := newRegistrationWorld(t)
			if point == "mark" || point == "commit" {
				w.chats.point = point
			} else {
				w.repo.point = point
			}
			if _, err := w.registrar.StageView(context.Background(), w.req); err == nil {
				t.Fatal("interruption not observed")
			}
			out, err := w.registrar.StageView(context.Background(), w.req)
			if err != nil {
				t.Fatal(err)
			}
			chat, err := w.f.chats.ReadCurrent(context.Background(), w.req.OwnerID, w.req.ChatID)
			if err != nil || !chat.SourceBindingConfirmed || chat.SourceBindingRevision != 1 || chat.ConnectionRevision != 1 || chat.DisplayName != "yanfa-erp" {
				t.Fatal("binding replay duplicated revision or failed confirmation")
			}
			source, err := w.f.repo.Get(context.Background(), out.SourceID)
			if err != nil || source.RecoveryVerifiedInstanceID != "" || source.Phase != models.ERPSourceRecovering || len(source.SyncOperatorAccountIDs) != 0 || source.ApprovalRefs["G15-0"] != "" || out.Receipt.Phase != "view_bound" {
				t.Fatal("registrar granted execution or recovery")
			}
			again, err := w.registrar.StageView(context.Background(), w.req)
			if err != nil || again.SourceID != out.SourceID || again.PublicConfigSHA256 != out.PublicConfigSHA256 {
				t.Fatal("same intent did not regenerate identical publication")
			}
			changed := w.req
			changed.OperatorApprovalRef = "changed-intent"
			if _, err := w.registrar.StageView(context.Background(), changed); err == nil {
				t.Fatal("changed intent overwrote receipt")
			}
		})
	}
}
func TestERPRegistrationPendingReceiptVetoesRealStartup(t *testing.T) {
	w := newRegistrationWorld(t)
	w.repo.point = "create"
	if _, err := w.registrar.StageView(context.Background(), w.req); err == nil {
		t.Fatal("interruption missing")
	}
	source, err := w.repo.FindRegistrationSource(context.Background(), w.req.Source.EnvironmentRef, w.req.Source.ApprovedEndpointAliases)
	if err != nil {
		t.Fatal(err)
	}
	w.f.service.syncRuntime = config.ERPSyncRuntime{Sources: map[string]config.ERPSourceRuntime{source.SourceID: w.req.Runtime}}
	service := NewDeploymentDataSyncService(w.f.service, repositories.NewDataSyncRepository(w.f.database))
	if service.Startup(context.Background()) != nil {
		t.Fatal("startup failed")
	}
	service.Shutdown(context.Background())
	source, err = w.f.repo.Get(context.Background(), source.SourceID)
	if err != nil || source.RegistrationReceipt.Phase != "view_pending" || source.Phase != models.ERPSourceRecovering || source.RecoveryVerifiedInstanceID != "" {
		t.Fatal("Startup completed pending registration")
	}
	if _, err := w.registrar.StageView(context.Background(), w.req); err != nil {
		t.Fatal(err)
	}
}

func TestERPRegistrationEnableLostAcknowledgementsDoNotBump(t *testing.T) {
	for _, point := range []string{"receipt", "state", "config"} {
		t.Run(point, func(t *testing.T) {
			w := newRegistrationWorld(t)
			stageA, err := w.registrar.StageView(context.Background(), w.req)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(filepath.Dir(w.req.PublicConfigPath), "stage-a.json")
			if os.WriteFile(path, registrationConfigBytes(stageA.PublicConfig), 0600) != nil {
				t.Fatal("A publication fixture")
			}
			access, err := NewSourceAccessService(w.repo, w.f.gate, stageA.PublicConfig, w.chats)
			if err != nil {
				t.Fatal(err)
			}
			service := NewDeploymentDataSyncService(access, repositories.NewDataSyncRepository(w.f.database))
			if service.Startup(context.Background()) != nil {
				t.Fatal("actual recovery startup failed")
			}
			service.Shutdown(context.Background())
			source, err := w.f.repo.Get(context.Background(), stageA.SourceID)
			if err != nil || source.Phase != models.ERPSourceIdle || source.RecoveryVerifiedInstanceID == "" {
				t.Fatal("actual A recovery unconfirmed")
			}
			runtime := w.req.Runtime
			runtime.G15ApprovalRef = "synthetic-negative-trust-G15"
			req := ERPRegistrationEnableRequest{RequestID: uuid.NewString(), StageViewRequestID: w.req.RequestID, SourceID: stageA.SourceID, PublicConfigPath: path, PublicConfigBeforeSHA256: stageA.PublicConfigSHA256, ExpectedConfigRevision: source.ConfigRevision, ExpectedStateRevision: source.StateRevision, ExpectedBindingRevision: 1, SyncOperatorAccountIDs: []primitive.ObjectID{w.req.OwnerID}, ApprovalRefs: map[string]string{"Q15-01": "synthetic", "Q15-02": "synthetic-view-only", "Q15-03": "synthetic", "Q15-04": "synthetic", "G15-0": runtime.G15ApprovalRef}, Runtime: runtime, StageARecoveryEvidenceRef: "actual-this-test-Startup", ApplicationReadEvidenceRef: "synthetic-negative-case-evidence", TrustedInstallationEvidenceRef: "synthetic-negative-trust-case", OperatorApprovalRef: "synthetic-second-window", LegacyWriterDisabledRef: "synthetic-no-legacy-writer"}
			registrar, err := NewERPSourceRegistrationService(access, repositories.NewDataSyncRepository(w.f.database), stageA.PublicConfig)
			if err != nil {
				t.Fatal(err)
			}
			w.repo.point = point
			if _, err := registrar.EnableSync(context.Background(), req); err == nil {
				t.Fatal("lost acknowledgement not observed")
			}
			output, err := registrar.EnableSync(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := w.f.repo.Get(context.Background(), stageA.SourceID)
			if err != nil || stored.ConfigRevision != req.ExpectedConfigRevision+1 || stored.StateRevision != req.ExpectedStateRevision+1 || stored.RecoveryVerifiedInstanceID != "" || stored.RegistrationReceipt.Phase != "sync_enabled" {
				t.Fatal("enable replay changed revisions or fabricated recovery")
			}
			again, err := registrar.EnableSync(context.Background(), req)
			if err != nil || again.PublicConfigSHA256 != output.PublicConfigSHA256 {
				t.Fatal("enable replay did not regenerate identical B")
			}
			conflict := req
			conflict.RequestID = uuid.NewString()
			if _, err := registrar.EnableSync(context.Background(), conflict); err == nil {
				t.Fatal("conflicting enable intent accepted")
			}
		})
	}
}
