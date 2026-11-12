package di

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/mongodb"
)

func registrationHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func registrationWrite(t *testing.T, path string, value interface{}) string {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if file.Sync() != nil {
		t.Fatal("fixture fsync")
	}
	file.Close()
	return registrationHash(raw)
}
func registrationCLI(t *testing.T, ctx context.Context, root, mode, input, outdir, uri, name, key string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/erp-source-register", mode, "--input", input, "--output-dir", outdir)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "YANFA_MONGODB_URI="+uri, "YANFA_MONGODB_NAME="+name, "SCHEMA_ENCRYPTION_KEY="+key)
	if _, err := cmd.Output(); err != nil {
		code, exitCode := "REGISTRATION_CLI_EXECUTION_FAILED", -1
		if failure, ok := err.(*exec.ExitError); ok {
			exitCode = failure.ExitCode()
			for _, line := range strings.Split(string(failure.Stderr), "\n") {
				line = strings.TrimSpace(line)
				switch line {
				case "REGISTRATION_INPUT_INVALID", "REGISTRATION_CONFLICT", "REGISTRATION_INFRASTRUCTURE_MISSING", "REGISTRATION_STATE_UNAVAILABLE", "REGISTRATION_GATE_UNAVAILABLE", "REGISTRATION_OUTPUT_CONFLICT",
					"SOURCE_BUSY", "SOURCE_LEASE_MISMATCH", "SOURCE_LEASE_MISSING", "SOURCE_LEASE_CLOSED", "SOURCE_GATE_UNSAFE_PATH",
					"ERP_SOURCE_UNRESOLVED", "ERP_SOURCE_FORBIDDEN", "ERP_SOURCE_STATE_UNAVAILABLE", "ERP_SOURCE_BINDING_CONFLICT", "ERP_SOURCE_CONFLICT", "ERP_SOURCE_INVALID",
					"DATA_SYNC_RECOVERING", "DATA_SYNC_IN_PROGRESS", "DATA_SYNC_CONFLICT", "DATA_SYNC_INVALID":
					code = line
				}
			}
		}
		t.Fatalf("actual registrar CLI failed: mode=%s code=%s exit_code=%d", mode, code, exitCode)
	}
}
func registrationPublication(t *testing.T, path string, candidate config.ERPSyncConfig) {
	t.Helper()
	temporary := path + "." + uuid.NewString()
	registrationWrite(t, temporary, candidate)
	if os.Rename(temporary, path) != nil {
		t.Fatal("atomic full-config publication")
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if directory.Sync() != nil {
		t.Fatal("publication directory fsync")
	}
}

// This regression uses the actual CLI, static configuration loader, production
// runner/verifier and Startup. It never replaces Load/Ready/Prepare. The final
// stage proves missing trusted installation denial, not a formal Ready positive.
func TestERPRegistrationActualCLIConfigurationStartupAndView(t *testing.T) {
	if os.Getenv("YANFA_T40_ALLOW_ISOLATED") != "owned-18545-18546" {
		t.Fatal("manager must explicitly authorize the isolated registration fixture")
	}
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	readerURL := os.Getenv("ERP_REGISTRATION_TEST_READER_URL")
	controlDSN := os.Getenv("YANFA_SYNC_TEST_CONTROL_DSN")
	parsed, err := url.Parse(readerURL)
	if err != nil || parsed.Hostname() != "127.0.0.1" || parsed.Port() != "18545" || parsed.Path != "/bi_agent" || parsed.User == nil || parsed.User.Username() != "v15_bridge_reader" || !strings.HasPrefix(uri, "mongodb://127.0.0.1:18546") || controlDSN == "" {
		t.Fatal("owned isolated Mongo and three-role PG inputs required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(3*time.Second))
	if err != nil {
		t.Fatal("isolated Mongo unavailable")
	}
	defer client.Disconnect(context.Background())
	if client.Ping(ctx, nil) != nil {
		t.Fatal("isolated Mongo unavailable")
	}
	name := "t40_registration_" + primitive.NewObjectID().Hex()
	database := client.Database(name)
	defer database.Drop(context.Background())
	wrapper := &mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: name}}
	chats := repositories.NewChatRepository(wrapper, nil)
	key := "0123456789abcdef0123456789abcdef"
	oldKey := config.Env.SchemaEncryptionKey
	config.Env.SchemaEncryptionKey = key
	defer func() { config.Env.SchemaEncryptionKey = oldKey }()
	port, reader, password, ssl := "18545", parsed.User.Username(), "", "disable"
	password, _ = parsed.User.Password()
	connection := models.Connection{Type: "postgresql", Host: "127.0.0.1", Port: &port, Username: &reader, Password: &password, Database: "bi_agent", SSLMode: &ssl}
	owner := primitive.NewObjectID()
	chat := models.NewChat(owner, connection.Clone(), models.DefaultChatSettings())
	if utils.EncryptConnection(&chat.Connection) != nil || chats.Create(chat) != nil {
		t.Fatal("existing encrypted Chat fixture failed")
	}
	home, _ := os.UserHomeDir()
	scratch, err := os.MkdirTemp(home, ".t40-di-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(scratch)
	controlPath := filepath.Join(scratch, "control.json")
	registrationWrite(t, controlPath, map[string]string{"dsn": controlDSN})
	uid := uint32(os.Getuid())
	cfg := config.ERPSyncConfig{InstanceID: "synthetic-t40-loader", DeploymentUID: uid, LockDirectory: fmt.Sprintf("/run/user/%d/yanfa-bi-chat/source-gates", uid), Sync: config.ERPSyncRuntime{Sources: map[string]config.ERPSourceRuntime{}, ControlSeconds: 3}}
	publicPath := filepath.Join(scratch, "public.json")
	beforeHash := registrationWrite(t, publicPath, cfg)
	mnt, _ := os.Readlink("/proc/self/ns/mnt")
	pid, _ := os.Readlink("/proc/self/ns/pid")
	runtime := config.ERPSourceRuntime{ManifestRef: "t45:f1474d4d6ecbd6a9e9c18b85d7a7d838aca948d3:077c3be10a76bc9251f68660db1e2e55b08807c077ddaaf615547d4d0ce3d282", PrivateEnvironment: config.SecretFileReference{Reference: "synthetic-private-not-installed", UID: uid, Mode: 0600}, ControlReader: config.SecretFileReference{Reference: "synthetic-control", Path: controlPath, UID: uid, Mode: 0600}, AftersaleSource: "erp.aftersale.list.query", RuntimeMountNamespace: mnt, RuntimePIDNamespace: pid, ReaderRoleByRef: map[string]string{"synthetic-reader": "v15_bridge_reader"}, WriterRole: "v15_bridge_writer", ControlRole: "v15_bridge_control", ObservationApprovalRef: "synthetic-owned-observation", SingleBackendApprovalRef: "synthetic-owned-offline"}
	view := services.ERPRegistrationViewRequest{RequestID: uuid.NewString(), PublicConfigPath: publicPath, PublicConfigBeforeSHA256: beforeHash, OwnerID: owner, ChatID: chat.ID, Source: models.ERPSource{EnvironmentRef: "synthetic-owned-18545", PhysicalDatabase: "bi_agent", ApprovedEndpointAliases: []string{services.EndpointFingerprint(connection)}, ApprovedConnectionFingerprints: []string{services.ConnectionFingerprint(connection)}, ReaderRoleRefs: []string{"synthetic-reader"}, WriterRoleRef: "synthetic-writer", ComponentManifestRef: runtime.ManifestRef, SecretInjectionRef: runtime.PrivateEnvironment.Reference, ViewAccountIDs: []primitive.ObjectID{owner}, ApprovalRefs: map[string]string{"Q15-02": "synthetic-view-only"}}, Runtime: runtime, OperatorApprovalRef: "synthetic-only-operator", LegacyWriterDisabledRef: "synthetic-no-legacy-entries"}
	view.Source.ShopScope = []string{"166754", "166879", "166889", "167156", "900511001", "900656971", "900698220"}
	view.Source.OrderSourceByShop = map[string]string{}
	for index, shop := range view.Source.ShopScope {
		source := "erp.trade.list.query"
		if index%2 == 1 {
			source = "erp.trade.outstock.simple.query"
		}
		view.Source.OrderSourceByShop[shop] = source
	}
	input := filepath.Join(scratch, "view-input.json")
	registrationWrite(t, input, view)
	outdir := filepath.Join(scratch, "receipts")
	if os.Mkdir(outdir, 0700) != nil {
		t.Fatal("receipt directory")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	registrationCLI(t, ctx, root, "stage-view", input, outdir, uri, name, key)
	var stageA services.ERPRegistrationOutput
	if services.ReadERPRegistrationJSON(filepath.Join(outdir, "stage-view-receipt.json"), &stageA) != nil {
		t.Fatal("actual receipt unavailable")
	}
	if stageA.SourceID == "" || stageA.Receipt.Phase != "view_bound" || stageA.PublicConfig.Sync.Sources[stageA.SourceID].G15ApprovalRef != "" {
		t.Fatal("stage A granted sync")
	}
	registrationPublication(t, publicPath, stageA.PublicConfig)
	t.Setenv("YANFA_ERP_SOURCE_CONFIG", publicPath)
	access, err := deploymentSourceAccess(chats, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	syncService := services.NewDeploymentDataSyncService(access, repositories.NewDataSyncRepository(database))
	if syncService.Startup(ctx) != nil {
		t.Fatal("actual Startup failed")
	}
	defer syncService.Shutdown(context.Background())
	current, err := chats.ReadCurrent(ctx, owner, chat.ID)
	if err != nil || current.SourceID != stageA.SourceID || !current.SourceBindingConfirmed {
		t.Fatal("actual generated UUID did not load")
	}
	status, err := syncService.Status(ctx, owner.Hex(), chat.ID.Hex(), "")
	if err != nil || status.CanStart || !status.AskAllowed || status.Readiness.State != "not_ready" {
		t.Fatal("actual loader did not separate view recovery and sync readiness")
	}
	sources := repositories.NewERPSourceRepository(database)
	stored, err := sources.Get(ctx, stageA.SourceID)
	if err != nil || stored.Phase != models.ERPSourceIdle || stored.RecoveryVerifiedInstanceID == "" || stored.RecoveryVerifiedInstanceID == cfg.InstanceID {
		t.Fatal("Startup nil was not backed by fresh persisted recovery")
	}
	read, err := access.AcquireRead(ctx, owner, current, connection)
	if err != nil {
		t.Fatal("actual SH read denied")
	}
	manager, err := dbmanager.NewManager(nil, key)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetSourceAccess(access)
	manager.RegisterDriver("postgresql", dbmanager.NewPostgresDriver())
	managerOpen := true
	defer func() {
		if managerOpen {
			manager.Close()
		}
	}()
	connect := dbmanager.ConnectionConfig{Type: "postgresql", Host: connection.Host, Port: connection.Port, Username: connection.Username, Password: connection.Password, Database: "bi_agent", SSLMode: &ssl, ChatID: chat.ID.Hex(), SourceChat: current, InitialSchemaDeferred: true}
	if manager.Connect(read.Context, chat.ID.Hex(), owner.Hex(), "", connect) != nil {
		t.Fatal("approved existing application connection failed")
	}
	executor, err := manager.GetConnectionContext(read.Context, chat.ID.Hex())
	if err != nil {
		t.Fatal("gated application executor unavailable")
	}
	var rows []struct {
		Database string
		Role     string
	}
	if executor.Query("SELECT current_database() AS database,current_user AS role", &rows) != nil || len(rows) != 1 || rows[0].Database != "bi_agent" || rows[0].Role != "v15_bridge_reader" {
		t.Fatal("application minimum read target differed")
	}
	read.Close()
	manager.Close()
	managerOpen = false
	if syncService.Shutdown(ctx) != nil {
		t.Fatal("stage A shutdown not joined")
	}
	stored, err = sources.Get(ctx, stageA.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	// Complete the offline CAS and publication using honestly synthetic evidence.
	// Missing installation still must fail the original runtime trust gate.
	enableRuntime := runtime
	enableRuntime.G15ApprovalRef = "synthetic-negative-trust-G15"
	enable := services.ERPRegistrationEnableRequest{RequestID: uuid.NewString(), StageViewRequestID: view.RequestID, SourceID: stageA.SourceID, PublicConfigPath: publicPath, PublicConfigBeforeSHA256: stageA.PublicConfigSHA256, ExpectedConfigRevision: stored.ConfigRevision, ExpectedStateRevision: stored.StateRevision, ExpectedBindingRevision: current.SourceBindingRevision, SyncOperatorAccountIDs: []primitive.ObjectID{owner}, ApprovalRefs: map[string]string{"Q15-01": "synthetic", "Q15-02": "synthetic-view-only", "Q15-03": "synthetic", "Q15-04": "synthetic", "G15-0": enableRuntime.G15ApprovalRef}, Runtime: enableRuntime, StageARecoveryEvidenceRef: "this-test-real-Startup", ApplicationReadEvidenceRef: "this-test-real-application-read", TrustedInstallationEvidenceRef: "synthetic-negative-trust-case-only", OperatorApprovalRef: "synthetic-second-window", LegacyWriterDisabledRef: "synthetic-no-legacy-entries"}
	enableInput := filepath.Join(scratch, "enable-input.json")
	registrationWrite(t, enableInput, enable)
	registrationCLI(t, ctx, root, "enable-sync", enableInput, outdir, uri, name, key)
	stored, err = sources.Get(ctx, stageA.SourceID)
	if err != nil || stored.ConfigRevision != enable.ExpectedConfigRevision+1 || stored.RecoveryVerifiedInstanceID != "" || stored.Phase != models.ERPSourceRecovering {
		t.Fatal("enable CAS retained old recovery authority")
	}
	registrationCLI(t, ctx, root, "enable-sync", enableInput, outdir, uri, name, key)
	repeated, _ := sources.Get(ctx, stageA.SourceID)
	if repeated.ConfigRevision != stored.ConfigRevision {
		t.Fatal("repeat enable bumped config")
	}
	// Source enabled while A remains published: original Load still refuses.
	unpublished, err := deploymentSourceAccess(chats, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	denied := services.NewDeploymentDataSyncService(unpublished, repositories.NewDataSyncRepository(database))
	if denied.Startup(ctx) != nil {
		t.Fatal("half-publication recovery failed")
	}
	half, err := denied.Status(ctx, owner.Hex(), chat.ID.Hex(), "")
	if err != nil || half.CanStart || half.Readiness.State != "not_ready" {
		t.Fatal("source-only G15 enabled execution")
	}
	denied.Shutdown(ctx)
	var stageB services.ERPRegistrationOutput
	if services.ReadERPRegistrationJSON(filepath.Join(outdir, "enable-sync-receipt.json"), &stageB) != nil {
		t.Fatal("B receipt unavailable")
	}
	registrationPublication(t, publicPath, stageB.PublicConfig)
	fresh, err := deploymentSourceAccess(chats, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	after := services.NewDeploymentDataSyncService(fresh, repositories.NewDataSyncRepository(database))
	if after.Startup(ctx) != nil {
		t.Fatal("B startup failed")
	}
	defer after.Shutdown(context.Background())
	final, err := after.Status(ctx, owner.Hex(), chat.ID.Hex(), "")
	if err != nil || final.CanStart || final.Readiness.State != "not_ready" || !final.AskAllowed {
		t.Fatal("untrusted B became executable or lost recovered view")
	}
	if _, err := after.CreateJob(ctx, owner.Hex(), chat.ID.Hex(), uuid.NewString()); err == nil {
		t.Fatal("missing trusted install launched writer")
	}
}
