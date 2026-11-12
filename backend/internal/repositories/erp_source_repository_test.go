package repositories

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yanfachatbi/internal/models"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func erpSourceMongo(t *testing.T) (*mongo.Database, ERPSourceRepository) {
	t.Helper()
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("registry persistence unverified: ASSISTANT_TEST_MONGO_URI is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Disconnect(context.Background()) })
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	database := client.Database("erp_source_test_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() { database.Drop(context.Background()) })
	repo := NewERPSourceRepository(database)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	return database, repo
}

func erpSourceRegistration(t *testing.T, alias string) *models.ERPSource {
	t.Helper()
	id := uuid.NewString()
	return &models.ERPSource{SchemaVersion: 1, SourceID: id, ConfigRevision: 1, EnvironmentRef: "isolated-test",
		PhysicalDatabase: "bi_agent", ApprovedEndpointAliases: []string{alias}, ApprovedConnectionFingerprints: []string{alias}, ReaderRoleRefs: []string{"isolated-reader"},
		WriterRoleRef: "isolated-writer-reference", DeploymentUID: uint32(os.Getuid()), LockPath: filepath.Join(t.TempDir(), id+".lock"), LockInode: 1,
		ComponentManifestRef: "isolated-manifest", ApprovalRefs: map[string]string{"Q15-02": "isolated-only-approval"}, Phase: models.ERPSourceRecovering}
}

func TestERPSourceRegistryRestartAndBindingCAS(t *testing.T) {
	database, repo := erpSourceMongo(t)
	ctx := context.Background()
	source := erpSourceRegistration(t, strings.Repeat("a", 64))
	if err := repo.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	// A new repository reads durable state; no in-memory rows survive here.
	restarted := NewERPSourceRepository(database)
	stored, err := restarted.Get(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != models.ERPSourceRecovering || stored.RecoveryVerifiedInstanceID != "" || len(stored.SyncOperatorAccountIDs) != 0 {
		t.Fatal("new registration became executable or granted operation rights")
	}
	binding := models.ERPChatBinding{ChatID: primitive.NewObjectID(), OwnerID: primitive.NewObjectID(), ConnectionFingerprint: strings.Repeat("a", 64), BindingRevision: 1, OperationID: uuid.NewString()}
	if err := repo.PutBindingCAS(ctx, source.SourceID, 1, nil, binding); err != nil {
		t.Fatal(err)
	}
	if err := restarted.PutBindingCAS(ctx, source.SourceID, 1, nil, binding); !errors.Is(err, ErrERPSourceConflict) {
		t.Fatalf("duplicate binding CAS: %v", err)
	}
	stored, err = restarted.Get(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.ChatBindings) != 1 || stored.ChatBindings[0].Confirmed {
		t.Fatal("partial binding was executable")
	}
	if err := repo.ConfirmBindingCAS(ctx, source.SourceID, 1, binding); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ConfirmBindingCAS(ctx, source.SourceID, 1, binding); !errors.Is(err, ErrERPSourceConflict) {
		t.Fatalf("stale confirmation CAS: %v", err)
	}
	confirmed := binding
	confirmed.Confirmed = true
	next := binding
	next.BindingRevision, next.OperationID = 2, uuid.NewString()
	if err := repo.PutBindingCAS(ctx, source.SourceID, 1, &confirmed, next); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RemoveBindingCAS(ctx, source.SourceID, 1, confirmed); !errors.Is(err, ErrERPSourceConflict) {
		t.Fatalf("stale remove deleted newer binding: %v", err)
	}
	stored, err = restarted.Get(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.ChatBindings) != 1 || stored.ChatBindings[0].BindingRevision != 2 || stored.ChatBindings[0].Confirmed {
		t.Fatal("binding CAS lost the unresolved successor")
	}
}

func TestERPSourceStateAndConfigCAS(t *testing.T) {
	_, repo := erpSourceMongo(t)
	ctx := context.Background()
	source := erpSourceRegistration(t, strings.Repeat("b", 64))
	if err := repo.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStateCAS(ctx, source.SourceID, 0, "", ERPSourceState{Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: "test-instance"}); err != nil {
		t.Fatal(err)
	}
	operation := &models.ERPSourceOperation{OperationID: uuid.NewString(), Kind: "sync", InstanceID: "test-instance"}
	if err := repo.UpdateStateCAS(ctx, source.SourceID, 1, "", ERPSourceState{Phase: models.ERPSourceSyncing, ActiveOperation: operation, RecoveryVerifiedInstanceID: "test-instance"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStateCAS(ctx, source.SourceID, 2, uuid.NewString(), ERPSourceState{Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: "test-instance"}); !errors.Is(err, ErrERPSourceConflict) {
		t.Fatalf("another operation cleared recovery fence: %v", err)
	}
	if err := repo.UpdateStateCAS(ctx, source.SourceID, 1, operation.OperationID, ERPSourceState{Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: "test-instance"}); !errors.Is(err, ErrERPSourceConflict) {
		t.Fatalf("stale revision cleared source: %v", err)
	}
	stored, err := repo.Get(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != models.ERPSourceSyncing || stored.ActiveOperation == nil || stored.ActiveOperation.OperationID != operation.OperationID {
		t.Fatal("failed CAS changed source recovery authority")
	}
	stored.ConfigRevision = 2
	stored.ViewAccountIDs = []primitive.ObjectID{primitive.NewObjectID()}
	// Config writes cannot overwrite an active operation or its state revision.
	stored.Phase, stored.ActiveOperation, stored.StateRevision = models.ERPSourceIdle, nil, 0
	if err := repo.UpdateConfigCAS(ctx, source.SourceID, 1, stored); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateConfigCAS(ctx, source.SourceID, 1, stored); !errors.Is(err, ErrERPSourceConflict) {
		t.Fatalf("stale config overwrite: %v", err)
	}
	stored, err = repo.Get(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != models.ERPSourceSyncing || stored.StateRevision != 2 || stored.ActiveOperation == nil {
		t.Fatal("config save erased active source operation")
	}
	duplicate := erpSourceRegistration(t, strings.Repeat("b", 64))
	if err := repo.Create(ctx, duplicate); !mongo.IsDuplicateKeyError(err) {
		t.Fatalf("ambiguous endpoint registration: %v", err)
	}
}

func TestERPRegistrationReceiptCASPreservesIdentityAndRelease(t *testing.T) {
	_, repo := erpSourceMongo(t)
	ctx := context.Background()
	source := erpSourceRegistration(t, strings.Repeat("8", 64))
	source.LastReleasedOperationID = uuid.NewString()
	receipt := &models.ERPRegistrationReceipt{RequestID: uuid.NewString(), InputSHA256: strings.Repeat("a", 64), ExistingChatID: primitive.NewObjectID(), OwnerID: primitive.NewObjectID(), BindingOperationID: uuid.NewString(), AfterBindingRevision: 1, AfterConfigRevision: 1, Phase: "view_pending"}
	source.RegistrationReceipt = receipt
	if err := repo.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	store := repo.(ERPSourceRegistrationStore)
	next := *receipt
	next.Revision++
	next.Phase = "view_bound"
	next.PublicConfigCandidateSHA256 = strings.Repeat("b", 64)
	if err := store.RegistrationCAS(ctx, source.SourceID, receipt, &next); err != nil {
		t.Fatal(err)
	}
	if err := store.RegistrationCAS(ctx, source.SourceID, receipt, &next); !errors.Is(err, ErrERPSourceConflict) {
		t.Fatal("stale receipt CAS accepted")
	}
	forged := next
	forged.Revision++
	forged.OwnerID = primitive.NewObjectID()
	if store.RegistrationCAS(ctx, source.SourceID, &next, &forged) == nil {
		t.Fatal("receipt changed existing owner")
	}
	stored, err := repo.Get(ctx, source.SourceID)
	if err != nil || stored.LastReleasedOperationID != source.LastReleasedOperationID || stored.RecoveryVerifiedInstanceID != "" || stored.ActiveOperation != nil {
		t.Fatal("receipt became busy/recovery authority or replaced release witness")
	}
}
