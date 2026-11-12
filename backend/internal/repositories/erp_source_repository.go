package repositories

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"

	"yanfachatbi/internal/models"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

var (
	ErrERPSourceConflict = errors.New("ERP_SOURCE_CAS_CONFLICT")
	ErrERPSourceInvalid  = errors.New("ERP_SOURCE_INVALID")
)

// ERPSourceRepository never caches authority in Redis. The primary/majority
// reads and writes are also used after acquiring flock and after a CAS.
type ERPSourceRepository interface {
	EnsureIndexes(context.Context) error
	Create(context.Context, *models.ERPSource) error
	Get(context.Context, string) (*models.ERPSource, error)
	List(context.Context) ([]models.ERPSource, error)
	PutBindingCAS(context.Context, string, int64, *models.ERPChatBinding, models.ERPChatBinding) error
	ConfirmBindingCAS(context.Context, string, int64, models.ERPChatBinding) error
	RemoveBindingCAS(context.Context, string, int64, models.ERPChatBinding) error
	UpdateConfigCAS(context.Context, string, int64, *models.ERPSource) error
	UpdateStateCAS(context.Context, string, int64, string, ERPSourceState) error
	ConfirmSyncPredecessors(context.Context, *models.ERPSource) error
}

type ERPSourceState struct {
	Phase                      models.ERPSourcePhase
	ActiveOperation            *models.ERPSourceOperation
	LastReleasedOperationID    string
	RecoveryVerifiedInstanceID string
	FrozenJob                  *models.DataSyncJob
}

type mongoERPSourceRepository struct {
	sources  *mongo.Collection
	syncJobs DataSyncRepository
}

func NewERPSourceRepository(database *mongo.Database) ERPSourceRepository {
	return &mongoERPSourceRepository{sources: database.Collection("erp_sources",
		options.Collection().SetReadPreference(readpref.Primary()).
			SetReadConcern(readconcern.Majority()).SetWriteConcern(writeconcern.Majority())), syncJobs: NewDataSyncRepository(database)}
}

func (r *mongoERPSourceRepository) ConfirmSyncPredecessors(ctx context.Context, source *models.ERPSource) error {
	if source == nil {
		return ErrDataSyncInvalid
	}
	jobs, err := r.syncJobs.ListUnreleased(ctx, source.SourceID)
	if err != nil {
		return err
	}
	return ConfirmSyncPredecessors(ctx, r.syncJobs, source, jobs)
}

func (r *mongoERPSourceRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.sources.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "source_id", Value: 1}}, Options: options.Index().SetUnique(true).SetName("erp_source_id")},
		{Keys: bson.D{{Key: "approved_endpoint_aliases", Value: 1}}, Options: options.Index().SetUnique(true).
			SetPartialFilterExpression(bson.M{"approved_endpoint_aliases.0": bson.M{"$exists": true}}).SetName("erp_endpoint_identity")},
		{Keys: bson.D{{Key: "chat_bindings.chat_id", Value: 1}}, Options: options.Index().SetName("erp_chat_binding")},
	})
	return err
}

func validERPSource(s *models.ERPSource) bool {
	if s == nil {
		return false
	}
	id, err := uuid.Parse(s.SourceID)
	if err != nil || id == uuid.Nil || id.String() != s.SourceID || s.SchemaVersion != 1 || s.ConfigRevision < 1 || s.StateRevision < 0 ||
		s.EnvironmentRef == "" || s.PhysicalDatabase != "bi_agent" || s.LockInode == 0 ||
		filepath.Base(s.LockPath) != s.SourceID+".lock" || !filepath.IsAbs(s.LockPath) ||
		len(s.ApprovedEndpointAliases) == 0 || len(s.ApprovedConnectionFingerprints) == 0 || len(s.ReaderRoleRefs) == 0 || s.WriterRoleRef == "" ||
		s.ComponentManifestRef == "" || s.ApprovalRefs["Q15-02"] == "" {
		return false
	}
	if s.RegistrationReceipt != nil && !validRegistrationReceipt(s.RegistrationReceipt) {
		return false
	}
	for _, aliases := range [][]string{s.ApprovedEndpointAliases, s.ApprovedConnectionFingerprints} {
		seen := make(map[string]bool, len(aliases))
		for _, alias := range aliases {
			if len(alias) != 64 || seen[alias] {
				return false
			}
			if _, err := hex.DecodeString(alias); err != nil {
				return false
			}
			seen[alias] = true
		}
	}
	return true
}

// Create accepts only the server-generated registration prepared by SourceAccess.
// No production caller or DI registration is installed by T37.
func (r *mongoERPSourceRepository) Create(ctx context.Context, source *models.ERPSource) error {
	if !validERPSource(source) || source.Phase != models.ERPSourceRecovering || source.ActiveOperation != nil || len(source.ChatBindings) != 0 || source.RecoveryVerifiedInstanceID != "" {
		return ErrERPSourceInvalid
	}
	if source.ChatBindings == nil {
		source.ChatBindings = []models.ERPChatBinding{}
	}
	_, err := r.sources.InsertOne(ctx, source)
	return err
}

func (r *mongoERPSourceRepository) Get(ctx context.Context, id string) (*models.ERPSource, error) {
	var source models.ERPSource
	if err := r.sources.FindOne(ctx, bson.M{"source_id": id}).Decode(&source); err != nil {
		return nil, err
	}
	if !validERPSource(&source) {
		return nil, ErrERPSourceInvalid
	}
	return &source, nil
}

func (r *mongoERPSourceRepository) List(ctx context.Context) ([]models.ERPSource, error) {
	cursor, err := r.sources.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var sources []models.ERPSource
	if err := cursor.All(ctx, &sources); err != nil {
		return nil, err
	}
	for i := range sources {
		if !validERPSource(&sources[i]) {
			return nil, ErrERPSourceInvalid
		}
	}
	return sources, nil
}

func validBinding(binding models.ERPChatBinding) bool {
	_, err := uuid.Parse(binding.OperationID)
	return !binding.ChatID.IsZero() && !binding.OwnerID.IsZero() && binding.BindingRevision > 0 && len(binding.ConnectionFingerprint) == 64 && err == nil
}

func bindingSelector(binding models.ERPChatBinding) bson.M {
	return bson.M{"chat_id": binding.ChatID, "owner_id": binding.OwnerID, "binding_revision": binding.BindingRevision,
		"connection_fingerprint": binding.ConnectionFingerprint, "operation_id": binding.OperationID, "confirmed": binding.Confirmed}
}

func (r *mongoERPSourceRepository) update(ctx context.Context, filter, update bson.M) error {
	result, err := r.sources.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrERPSourceConflict
	}
	return nil
}

func (r *mongoERPSourceRepository) PutBindingCAS(ctx context.Context, id string, configRevision int64, previous *models.ERPChatBinding, next models.ERPChatBinding) error {
	if !validBinding(next) || next.Confirmed {
		return ErrERPSourceInvalid
	}
	filter := bson.M{"source_id": id, "config_revision": configRevision}
	if previous == nil {
		filter["chat_bindings"] = bson.M{"$not": bson.M{"$elemMatch": bson.M{"chat_id": next.ChatID}}}
		return r.update(ctx, filter, bson.M{"$push": bson.M{"chat_bindings": next}})
	}
	if previous.ChatID != next.ChatID || previous.OwnerID != next.OwnerID || next.BindingRevision <= previous.BindingRevision {
		return ErrERPSourceInvalid
	}
	filter["chat_bindings"] = bson.M{"$elemMatch": bindingSelector(*previous)}
	return r.update(ctx, filter, bson.M{"$set": bson.M{"chat_bindings.$": next}})
}

func (r *mongoERPSourceRepository) ConfirmBindingCAS(ctx context.Context, id string, configRevision int64, pending models.ERPChatBinding) error {
	if !validBinding(pending) || pending.Confirmed {
		return ErrERPSourceInvalid
	}
	return r.update(ctx, bson.M{"source_id": id, "config_revision": configRevision,
		"chat_bindings": bson.M{"$elemMatch": bindingSelector(pending)}}, bson.M{"$set": bson.M{"chat_bindings.$.confirmed": true}})
}

func (r *mongoERPSourceRepository) RemoveBindingCAS(ctx context.Context, id string, configRevision int64, previous models.ERPChatBinding) error {
	if !validBinding(previous) {
		return ErrERPSourceInvalid
	}
	return r.update(ctx, bson.M{"source_id": id, "config_revision": configRevision,
		"chat_bindings": bson.M{"$elemMatch": bindingSelector(previous)}}, bson.M{"$pull": bson.M{"chat_bindings": bindingSelector(previous)}})
}

// The whitelist deliberately excludes identity, inode, bindings and state.
func (r *mongoERPSourceRepository) UpdateConfigCAS(ctx context.Context, id string, revision int64, next *models.ERPSource) error {
	if !validERPSource(next) || next.SourceID != id || next.ConfigRevision != revision+1 {
		return ErrERPSourceInvalid
	}
	return r.update(ctx, bson.M{"source_id": id, "config_revision": revision, "lock_device": next.LockDevice,
		"lock_inode": next.LockInode, "lock_path": next.LockPath, "deployment_uid": next.DeploymentUID}, bson.M{"$set": bson.M{
		"config_revision": next.ConfigRevision, "environment_ref": next.EnvironmentRef,
		"approved_endpoint_aliases": next.ApprovedEndpointAliases, "reader_role_refs": next.ReaderRoleRefs,
		"approved_connection_fingerprints": next.ApprovedConnectionFingerprints,
		"writer_role_ref":                  next.WriterRoleRef, "component_manifest_ref": next.ComponentManifestRef,
		"secret_injection_ref": next.SecretInjectionRef, "view_account_ids": next.ViewAccountIDs,
		"sync_operator_account_ids": next.SyncOperatorAccountIDs, "shop_scope": next.ShopScope,
		"order_source_by_shop": next.OrderSourceByShop, "approval_refs": next.ApprovalRefs,
	}})
}

func (r *mongoERPSourceRepository) UpdateStateCAS(ctx context.Context, id string, revision int64, operationID string, next ERPSourceState) error {
	if next.Phase != models.ERPSourceIdle && next.Phase != models.ERPSourceSyncing && next.Phase != models.ERPSourceRecovering {
		return ErrERPSourceInvalid
	}
	if next.Phase == models.ERPSourceIdle && (next.ActiveOperation != nil || next.RecoveryVerifiedInstanceID == "") {
		return ErrERPSourceInvalid
	}
	if next.Phase == models.ERPSourceSyncing && (next.ActiveOperation == nil || next.ActiveOperation.OperationID == "") {
		return ErrERPSourceInvalid
	}
	filter := bson.M{"source_id": id, "state_revision": revision}
	if operationID == "" {
		filter["active_operation"] = nil
	} else {
		filter["active_operation.operation_id"] = operationID
	}
	var active interface{} = next.ActiveOperation
	if next.ActiveOperation != nil && next.FrozenJob != nil {
		j := next.FrozenJob
		if !validSyncJob(j) || j.SourceID != id || !j.MatchesOperation(next.ActiveOperation) || j.Status != models.DataSyncCreated || j.Revision != 0 {
			return ErrERPSourceInvalid
		}
		raw, err := bson.Marshal(next.ActiveOperation)
		if err != nil {
			return ErrERPSourceInvalid
		}
		var document bson.M
		if err := bson.Unmarshal(raw, &document); err != nil {
			return ErrERPSourceInvalid
		}
		document["frozen_job"] = j
		active = document
	}
	set := bson.M{"phase": next.Phase, "last_released_operation_id": next.LastReleasedOperationID, "recovery_verified_instance_id": next.RecoveryVerifiedInstanceID}
	// Phase-only transitions retain the original immutable recovery snapshot.
	if next.ActiveOperation == nil || operationID != next.ActiveOperation.OperationID || next.FrozenJob != nil {
		set["active_operation"] = active
	}
	return r.update(ctx, filter, bson.M{"$inc": bson.M{"state_revision": int64(1)}, "$set": set})
}

// SyncFenceStore is separate from the binding repository contract. Its snapshot
// survives the source-write/job-insert failure window without re-freezing config.
type SyncFenceStore interface {
	FrozenSyncJob(context.Context, string, string) (*models.DataSyncJob, error)
}

func (r *mongoERPSourceRepository) FrozenSyncJob(ctx context.Context, source, operation string) (*models.DataSyncJob, error) {
	var stored struct {
		Active struct {
			models.ERPSourceOperation `bson:",inline"`
			Job                       *models.DataSyncJob `bson:"frozen_job"`
		} `bson:"active_operation"`
	}
	err := r.sources.FindOne(ctx, bson.M{"source_id": source, "active_operation.operation_id": operation}).Decode(&stored)
	if err != nil {
		return nil, err
	}
	if !validSyncJob(stored.Active.Job) || stored.Active.Job.SourceID != source || !stored.Active.Job.MatchesOperation(&stored.Active.ERPSourceOperation) {
		return nil, ErrDataSyncInvalid
	}
	return stored.Active.Job, nil
}

type ERPSourceRegistrationStore interface {
	FindRegistrationSource(context.Context, string, []string) (*models.ERPSource, error)
	RegistrationCAS(context.Context, string, *models.ERPRegistrationReceipt, *models.ERPRegistrationReceipt) error
}

func validRegistrationReceipt(r *models.ERPRegistrationReceipt) bool {
	return r != nil && r.Revision >= 0 && models.CanonicalSyncUUID(r.RequestID) && models.CanonicalSyncUUID(r.BindingOperationID) && len(r.InputSHA256) == 64 && !r.ExistingChatID.IsZero() && !r.OwnerID.IsZero() && r.AfterBindingRevision == r.BeforeBindingRevision+1 && r.AfterConfigRevision > 0 && (r.Phase == "view_pending" || r.Phase == "view_bound" || r.Phase == "sync_enabled")
}
func (r *mongoERPSourceRepository) FindRegistrationSource(ctx context.Context, environment string, aliases []string) (*models.ERPSource, error) {
	var source models.ERPSource
	err := r.sources.FindOne(ctx, bson.M{"environment_ref": environment, "approved_endpoint_aliases": bson.M{"$in": aliases}}).Decode(&source)
	if err != nil {
		return nil, err
	}
	if !validERPSource(&source) {
		return nil, ErrERPSourceInvalid
	}
	return &source, nil
}
func (r *mongoERPSourceRepository) RegistrationCAS(ctx context.Context, id string, previous, next *models.ERPRegistrationReceipt) error {
	if previous == nil {
		if !validRegistrationReceipt(next) || next.Revision != 0 || next.Phase != "view_pending" {
			return ErrERPSourceInvalid
		}
		return r.update(ctx, bson.M{"source_id": id, "registration_receipt": nil, "config_revision": next.BeforeConfigRevision, "state_revision": next.BeforeStateRevision, "active_operation": nil}, bson.M{"$set": bson.M{"registration_receipt": next, "phase": models.ERPSourceRecovering, "recovery_verified_instance_id": ""}, "$inc": bson.M{"state_revision": int64(1)}})
	}
	if !validRegistrationReceipt(previous) || !validRegistrationReceipt(next) || next.Revision != previous.Revision+1 {
		return ErrERPSourceInvalid
	}
	a, b := *previous, *next
	a.Revision, b.Revision = 0, 0
	a.Phase, b.Phase = "", ""
	a.PublicConfigCandidateSHA256, b.PublicConfigCandidateSHA256 = "", ""
	a.EnableRequestID, b.EnableRequestID = "", ""
	a.EnableInputSHA256, b.EnableInputSHA256 = "", ""
	a.EnableBeforeConfigRevision, b.EnableBeforeConfigRevision = 0, 0
	a.EnableBeforeStateRevision, b.EnableBeforeStateRevision = 0, 0
	a.EnablePhase, b.EnablePhase = "", ""
	a.EnableCandidateSHA256, b.EnableCandidateSHA256 = "", ""
	if !reflect.DeepEqual(a, b) || previous.Phase == "sync_enabled" && next.Phase != "sync_enabled" || previous.Phase == "view_bound" && next.Phase == "view_pending" || previous.EnableRequestID != "" && (next.EnableRequestID != previous.EnableRequestID || next.EnableInputSHA256 != previous.EnableInputSHA256 || next.EnableBeforeConfigRevision != previous.EnableBeforeConfigRevision || next.EnableBeforeStateRevision != previous.EnableBeforeStateRevision) {
		return ErrERPSourceInvalid
	}
	return r.update(ctx, bson.M{"source_id": id, "registration_receipt.revision": previous.Revision, "registration_receipt.request_id": previous.RequestID, "registration_receipt.input_sha256": previous.InputSHA256, "registration_receipt.phase": previous.Phase}, bson.M{"$set": bson.M{"registration_receipt": next}})
}
