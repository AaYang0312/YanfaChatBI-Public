package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"yanfachatbi/config"
	"yanfachatbi/internal/erpsyncinstall"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/sourcegate"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sys/unix"
)

var ErrERPRegistrationConflict = errors.New("REGISTRATION_CONFLICT")
var ErrERPRegistrationInput = errors.New("REGISTRATION_INPUT_INVALID")

type ERPRegistrationViewRequest struct {
	RequestID                  string                  `json:"request_id"`
	PublicConfigPath           string                  `json:"public_config_path"`
	PublicConfigBeforeSHA256   string                  `json:"public_config_before_sha256"`
	OwnerID                    primitive.ObjectID      `json:"owner_id"`
	ChatID                     primitive.ObjectID      `json:"chat_id"`
	ExpectedBindingRevision    int64                   `json:"expected_binding_revision"`
	ExpectedConnectionRevision int64                   `json:"expected_connection_revision"`
	ExpectedConfigRevision     int64                   `json:"expected_config_revision"`
	ExpectedStateRevision      int64                   `json:"expected_state_revision"`
	Source                     models.ERPSource        `json:"source"`
	Runtime                    config.ERPSourceRuntime `json:"runtime"`
	OperatorApprovalRef        string                  `json:"operator_approval_ref"`
	LegacyWriterDisabledRef    string                  `json:"legacy_writer_disabled_ref"`
}
type ERPRegistrationEnableRequest struct {
	RequestID                      string                  `json:"request_id"`
	StageViewRequestID             string                  `json:"stage_view_request_id"`
	SourceID                       string                  `json:"source_id"`
	PublicConfigPath               string                  `json:"public_config_path"`
	PublicConfigBeforeSHA256       string                  `json:"public_config_before_sha256"`
	ExpectedConfigRevision         int64                   `json:"expected_config_revision"`
	ExpectedStateRevision          int64                   `json:"expected_state_revision"`
	ExpectedBindingRevision        int64                   `json:"expected_binding_revision"`
	SyncOperatorAccountIDs         []primitive.ObjectID    `json:"sync_operator_account_ids"`
	ApprovalRefs                   map[string]string       `json:"approval_refs"`
	Runtime                        config.ERPSourceRuntime `json:"runtime"`
	StageARecoveryEvidenceRef      string                  `json:"stage_a_recovery_evidence_ref"`
	ApplicationReadEvidenceRef     string                  `json:"application_read_evidence_ref"`
	TrustedInstallationEvidenceRef string                  `json:"trusted_installation_evidence_ref"`
	OperatorApprovalRef            string                  `json:"operator_approval_ref"`
	LegacyWriterDisabledRef        string                  `json:"legacy_writer_disabled_ref"`
}
type ERPRegistrationOutput struct {
	SourceID                 string                         `json:"source_id"`
	PublicConfigBeforeSHA256 string                         `json:"public_config_before_sha256"`
	PublicConfigSHA256       string                         `json:"public_config_sha256"`
	PublicConfig             config.ERPSyncConfig           `json:"public_config"`
	Receipt                  *models.ERPRegistrationReceipt `json:"receipt"`
}

type ERPSourceRegistrationService struct {
	access *SourceAccessService
	jobs   repositories.DataSyncRepository
	store  repositories.ERPSourceRegistrationStore
	cfg    config.ERPSyncConfig
}

func NewERPSourceRegistrationService(access *SourceAccessService, jobs repositories.DataSyncRepository, cfg config.ERPSyncConfig) (*ERPSourceRegistrationService, error) {
	if access == nil || access.repo == nil || jobs == nil || cfg.ValidateDeployment() != nil || access.lockDirectory != cfg.LockDirectory || access.uid != cfg.DeploymentUID {
		return nil, ErrERPRegistrationInput
	}
	store, ok := access.repo.(repositories.ERPSourceRegistrationStore)
	if !ok {
		return nil, ErrERPRegistrationInput
	}
	return &ERPSourceRegistrationService{access: access, jobs: jobs, store: store, cfg: cfg}, nil
}
func ReadERPRegistrationJSON(path string, target interface{}) error {
	f, err := openSyncFile(path, uint32(os.Geteuid()), 0, false)
	if err != nil {
		return ErrERPRegistrationInput
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(raw) > 1<<20 || erpsyncinstall.DecodeJSON(raw, target) != nil {
		return ErrERPRegistrationInput
	}
	return nil
}
func LoadERPRegistrationConfig(path string) (config.ERPSyncConfig, string, error) {
	var cfg config.ERPSyncConfig
	raw, err := erpsyncinstall.ReadPublicFile(path, uint32(os.Geteuid()), 1<<20)
	if err != nil || erpsyncinstall.DecodeJSON(raw, &cfg) != nil || cfg.ValidateDeployment() != nil {
		return cfg, "", ErrERPRegistrationInput
	}
	return cfg, syncDigest(raw), nil
}
func registrationDigest(value interface{}) string {
	raw, _ := json.Marshal(value)
	return syncDigest(raw)
}
func registrationConfigBytes(cfg config.ERPSyncConfig) []byte {
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	return append(raw, '\n')
}
func sameStrings(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}
func (s *ERPSourceRegistrationService) offline() (*os.File, error) {
	claim := &DataSyncService{access: s.access}
	if claim.claimInstance() != nil {
		return nil, ErrSourceRecovering
	}
	return claim.instanceFile, nil
}
func (s *ERPSourceRegistrationService) confirmOffline(ctx context.Context, source *models.ERPSource, lease *sourcegate.Lease, runtime config.ERPSourceRuntime) error {
	if source.ActiveOperation != nil {
		return ErrSourceRecovering
	}
	mnt, mntErr := syncNamespace("mnt")
	pid, pidErr := syncNamespace("pid")
	if mntErr != nil || pidErr != nil || runtime.RuntimeMountNamespace != mnt || runtime.RuntimePIDNamespace != pid {
		return ErrSourceRecovering
	}
	jobs, err := s.jobs.ListUnreleased(ctx, source.SourceID)
	if err != nil || repositories.ConfirmSyncPredecessors(ctx, s.jobs, source, jobs) != nil {
		return ErrSourceRecovering
	}
	observer := NewERPSyncVerifier(config.ERPSyncRuntime{ControlSeconds: s.cfg.Sync.ControlSeconds, Sources: map[string]config.ERPSourceRuntime{source.SourceID: runtime}}, s.access.instanceID)
	if observer.Observe(ctx, source, nil, lease) != nil {
		return ErrSourceRecovering
	}
	return nil
}
func (s *ERPSourceRegistrationService) saveReceipt(ctx context.Context, source *models.ERPSource, next *models.ERPRegistrationReceipt) (*models.ERPSource, error) {
	err := s.store.RegistrationCAS(ctx, source.SourceID, source.RegistrationReceipt, next)
	fresh, readErr := s.access.source(ctx, source.SourceID)
	if readErr != nil {
		return nil, readErr
	}
	if !reflect.DeepEqual(fresh.RegistrationReceipt, next) {
		if err != nil {
			return nil, err
		}
		return nil, ErrERPRegistrationConflict
	}
	return fresh, nil
}
func (s *ERPSourceRegistrationService) candidate(source *models.ERPSource, runtime config.ERPSourceRuntime) (config.ERPSyncConfig, error) {
	// Clone the entire document: no mutation of another source's runtime/maps.
	raw, _ := json.Marshal(s.cfg)
	var cfg config.ERPSyncConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return cfg, ErrERPRegistrationInput
	}
	associations := make([]config.ERPAssociation, 0, len(cfg.Associations)+1)
	for _, a := range cfg.Associations {
		if a.SourceID == source.SourceID {
			continue
		}
		remaining := make([]string, 0, len(a.EndpointFingerprints))
		for _, endpoint := range a.EndpointFingerprints {
			if containsString(source.ApprovedEndpointAliases, endpoint) {
				if a.SourceID != "" {
					return cfg, ErrERPRegistrationConflict
				}
			} else {
				remaining = append(remaining, endpoint)
			}
		}
		if len(remaining) > 0 {
			a.EndpointFingerprints = remaining
			associations = append(associations, a)
		}
	}
	associations = append(associations, config.ERPAssociation{SourceID: source.SourceID, EndpointFingerprints: source.ApprovedEndpointAliases, ApprovalRef: source.ApprovalRefs["Q15-02"]})
	cfg.Associations = associations
	if cfg.Sync.Sources == nil {
		cfg.Sync.Sources = map[string]config.ERPSourceRuntime{}
	}
	cfg.Sync.Sources[source.SourceID] = runtime
	return cfg, cfg.ValidateDeployment()
}
func validViewRuntime(source *models.ERPSource, runtime config.ERPSourceRuntime, uid uint32) bool {
	return runtime.ManifestRef == source.ComponentManifestRef && runtime.PrivateEnvironment.Reference == source.SecretInjectionRef && runtime.G15ApprovalRef == "" && runtime.ControlReader.UID == uid && runtime.ControlReader.Mode == 0600 && runtime.ControlReader.Reference != "" && runtime.ObservationApprovalRef != "" && runtime.SingleBackendApprovalRef != "" && runtime.RuntimeMountNamespace != "" && runtime.RuntimePIDNamespace != "" && runtime.AftersaleSource != ""
}
func (s *ERPSourceRegistrationService) StageView(ctx context.Context, req ERPRegistrationViewRequest) (*ERPRegistrationOutput, error) {
	if !models.CanonicalSyncUUID(req.RequestID) || req.OwnerID.IsZero() || req.ChatID.IsZero() || req.OperatorApprovalRef == "" || req.LegacyWriterDisabledRef == "" || len(req.PublicConfigBeforeSHA256) != 64 || req.Source.SourceID != "" || req.Source.RegistrationReceipt != nil || req.Source.ApprovalRefs["Q15-02"] == "" || req.Source.ApprovalRefs["G15-0"] != "" || len(req.Source.SyncOperatorAccountIDs) != 0 || !accountAllowed(req.Source.ViewAccountIDs, req.OwnerID) || !validViewRuntime(&req.Source, req.Runtime, s.access.uid) {
		return nil, ErrERPRegistrationInput
	}
	loaded, publicHash, loadErr := LoadERPRegistrationConfig(req.PublicConfigPath)
	if loadErr != nil || !reflect.DeepEqual(loaded, s.cfg) {
		return nil, ErrERPRegistrationConflict
	}
	instance, err := s.offline()
	if err != nil {
		return nil, err
	}
	defer instance.Close()
	if s.access.repo.EnsureIndexes(ctx) != nil || s.jobs.EnsureIndexes(ctx) != nil {
		return nil, ErrSourceStateUnavailable
	}
	chat, err := s.access.chats.ReadCurrent(ctx, req.OwnerID, req.ChatID)
	if err != nil {
		return nil, ErrERPRegistrationConflict
	}
	plain := chat.Connection.Clone()
	utils.DecryptConnection(&plain)
	fingerprint := ConnectionFingerprint(plain)
	if plain.Database != "bi_agent" || !containsString(req.Source.ApprovedEndpointAliases, EndpointFingerprint(plain)) || !containsString(req.Source.ApprovedConnectionFingerprints, fingerprint) {
		return nil, ErrERPRegistrationConflict
	}
	digest := registrationDigest(req)
	source, err := s.store.FindRegistrationSource(ctx, req.Source.EnvironmentRef, req.Source.ApprovedEndpointAliases)
	newSource := errors.Is(err, mongo.ErrNoDocuments)
	if err != nil && !newSource {
		return nil, err
	}
	if publicHash != req.PublicConfigBeforeSHA256 && (newSource || source.RegistrationReceipt == nil || publicHash != source.RegistrationReceipt.PublicConfigCandidateSHA256) {
		return nil, ErrERPRegistrationConflict
	}
	if newSource {
		if req.ExpectedConfigRevision != 0 || req.ExpectedStateRevision != 0 || chat.SourceID != "" || chat.SourceBindingRevision != req.ExpectedBindingRevision || chat.ConnectionRevision != req.ExpectedConnectionRevision {
			return nil, ErrERPRegistrationConflict
		}
		source, err = s.access.prepareRegistration(req.Source)
		if err != nil {
			return nil, err
		}
		source.RegistrationReceipt = &models.ERPRegistrationReceipt{RequestID: req.RequestID, InputSHA256: digest, ExistingChatID: req.ChatID, OwnerID: req.OwnerID, BindingOperationID: uuid.NewString(), BeforeBindingRevision: req.ExpectedBindingRevision, AfterBindingRevision: req.ExpectedBindingRevision + 1, BeforeConnectionRevision: req.ExpectedConnectionRevision, BeforeConfigRevision: 0, AfterConfigRevision: 1, Phase: "view_pending", PublicConfigBeforeSHA256: req.PublicConfigBeforeSHA256, ApprovalRefs: map[string]string{"Q15-02": req.Source.ApprovalRefs["Q15-02"], "operator": req.OperatorApprovalRef, "legacy_writer_disabled": req.LegacyWriterDisabledRef}}
	} else {
		if !sameStrings(source.ApprovedEndpointAliases, req.Source.ApprovedEndpointAliases) || !sameStrings(source.ApprovedConnectionFingerprints, req.Source.ApprovedConnectionFingerprints) || source.PhysicalDatabase != req.Source.PhysicalDatabase || source.EnvironmentRef != req.Source.EnvironmentRef {
			return nil, ErrERPRegistrationConflict
		}
		if source.RegistrationReceipt == nil {
			// Existing sources may be rebound only in the exact expected offline window.
			if source.ConfigRevision != req.ExpectedConfigRevision || source.StateRevision != req.ExpectedStateRevision || chat.SourceBindingRevision != req.ExpectedBindingRevision || chat.ConnectionRevision != req.ExpectedConnectionRevision || chat.SourceID != "" && chat.SourceID != source.SourceID {
				return nil, ErrERPRegistrationConflict
			}
		} else if source.RegistrationReceipt.RequestID != req.RequestID || source.RegistrationReceipt.InputSHA256 != digest {
			return nil, ErrERPRegistrationConflict
		}
	}
	op := uuid.NewString()
	if source.RegistrationReceipt != nil {
		op = source.RegistrationReceipt.BindingOperationID
	}
	identity := sourcegate.Identity{SourceID: source.SourceID, OperationID: op, OwnerID: req.OwnerID.Hex(), ChatID: req.ChatID.Hex(), BindingRevision: req.ExpectedBindingRevision + 1, ConnectionFingerprint: fingerprint, ConfigRevision: source.ConfigRevision}
	lease, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, identity)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	if !newSource {
		fresh, readErr := s.access.source(ctx, source.SourceID)
		if readErr != nil || fresh.ConfigRevision != source.ConfigRevision || fresh.StateRevision != source.StateRevision || !reflect.DeepEqual(fresh.RegistrationReceipt, source.RegistrationReceipt) {
			return nil, ErrERPRegistrationConflict
		}
		source = fresh
	}
	if s.confirmOffline(ctx, source, lease, req.Runtime) != nil {
		return nil, ErrSourceRecovering
	}
	if newSource {
		createErr := s.access.repo.Create(ctx, source)
		fresh, readErr := s.store.FindRegistrationSource(ctx, req.Source.EnvironmentRef, req.Source.ApprovedEndpointAliases)
		if readErr != nil {
			if createErr != nil {
				return nil, createErr
			}
			return nil, readErr
		}
		if fresh.SourceID != source.SourceID || fresh.RegistrationReceipt == nil || fresh.RegistrationReceipt.InputSHA256 != digest {
			return nil, ErrERPRegistrationConflict
		}
		source = fresh
	} else if source.RegistrationReceipt == nil {
		receipt := &models.ERPRegistrationReceipt{RequestID: req.RequestID, InputSHA256: digest, ExistingChatID: req.ChatID, OwnerID: req.OwnerID, BindingOperationID: op, BeforeBindingRevision: req.ExpectedBindingRevision, AfterBindingRevision: req.ExpectedBindingRevision + 1, BeforeConnectionRevision: req.ExpectedConnectionRevision, BeforeConfigRevision: source.ConfigRevision, BeforeStateRevision: source.StateRevision, AfterConfigRevision: source.ConfigRevision + 1, Phase: "view_pending", PublicConfigBeforeSHA256: req.PublicConfigBeforeSHA256, ApprovalRefs: map[string]string{"Q15-02": req.Source.ApprovalRefs["Q15-02"], "operator": req.OperatorApprovalRef, "legacy_writer_disabled": req.LegacyWriterDisabledRef}}
		source, err = s.saveReceipt(ctx, source, receipt)
		if err != nil {
			return nil, err
		}
	}
	if source.RegistrationReceipt.Phase == "sync_enabled" {
		return nil, ErrERPRegistrationConflict
	}
	if source.ConfigRevision < source.RegistrationReceipt.AfterConfigRevision {
		next := *source
		next.ConfigRevision++
		next.ViewAccountIDs = req.Source.ViewAccountIDs
		next.SyncOperatorAccountIDs = nil
		next.ApprovalRefs = req.Source.ApprovalRefs
		next.ComponentManifestRef = req.Source.ComponentManifestRef
		next.SecretInjectionRef = req.Source.SecretInjectionRef
		next.ReaderRoleRefs = req.Source.ReaderRoleRefs
		next.WriterRoleRef = req.Source.WriterRoleRef
		next.ShopScope = req.Source.ShopScope
		next.OrderSourceByShop = req.Source.OrderSourceByShop
		writeErr := s.access.repo.UpdateConfigCAS(ctx, source.SourceID, source.ConfigRevision, &next)
		fresh, readErr := s.access.source(ctx, source.SourceID)
		if readErr != nil {
			return nil, readErr
		}
		if !sameRegistrationConfig(fresh, &next) {
			if writeErr != nil {
				return nil, writeErr
			}
			return nil, ErrERPRegistrationConflict
		}
		source = fresh
	}
	if source.ConfigRevision != source.RegistrationReceipt.AfterConfigRevision {
		return nil, ErrERPRegistrationConflict
	}
	source, err = s.bindView(ctx, source, req, fingerprint, lease)
	if err != nil {
		return nil, err
	}
	candidate, err := s.candidate(source, req.Runtime)
	if err != nil {
		return nil, err
	}
	hash := syncDigest(registrationConfigBytes(candidate))
	receipt := *source.RegistrationReceipt
	if receipt.PublicConfigCandidateSHA256 != "" && receipt.PublicConfigCandidateSHA256 != hash {
		return nil, ErrERPRegistrationConflict
	}
	if receipt.Phase != "view_bound" || receipt.PublicConfigCandidateSHA256 == "" {
		receipt.Revision++
		receipt.Phase = "view_bound"
		receipt.PublicConfigCandidateSHA256 = hash
		source, err = s.saveReceipt(ctx, source, &receipt)
		if err != nil {
			return nil, err
		}
	}
	return &ERPRegistrationOutput{SourceID: source.SourceID, PublicConfigBeforeSHA256: req.PublicConfigBeforeSHA256, PublicConfigSHA256: hash, PublicConfig: candidate, Receipt: source.RegistrationReceipt}, nil
}
func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func sameRegistrationConfig(a, b *models.ERPSource) bool {
	return a.ConfigRevision == b.ConfigRevision && a.ComponentManifestRef == b.ComponentManifestRef && a.SecretInjectionRef == b.SecretInjectionRef && reflect.DeepEqual(a.ViewAccountIDs, b.ViewAccountIDs) && reflect.DeepEqual(a.SyncOperatorAccountIDs, b.SyncOperatorAccountIDs) && reflect.DeepEqual(a.ApprovalRefs, b.ApprovalRefs) && reflect.DeepEqual(a.ReaderRoleRefs, b.ReaderRoleRefs) && a.WriterRoleRef == b.WriterRoleRef && reflect.DeepEqual(a.ShopScope, b.ShopScope) && reflect.DeepEqual(a.OrderSourceByShop, b.OrderSourceByShop)
}
func (s *ERPSourceRegistrationService) bindView(ctx context.Context, source *models.ERPSource, req ERPRegistrationViewRequest, fingerprint string, lease *sourcegate.Lease) (*models.ERPSource, error) {
	r := source.RegistrationReceipt
	chat, err := s.access.chats.ReadCurrent(ctx, req.OwnerID, req.ChatID)
	if err != nil {
		return nil, err
	}
	plain := chat.Connection.Clone()
	utils.DecryptConnection(&plain)
	if ConnectionFingerprint(plain) != fingerprint {
		return nil, ErrERPRegistrationConflict
	}
	pending := *chat
	pending.SourceID = source.SourceID
	pending.ERPAssociated = true
	pending.SourceBindingConfirmed = false
	pending.SourceBindingRevision = r.AfterBindingRevision
	pending.ConnectionRevision = r.BeforeConnectionRevision + 1
	pending.SourceConnectionFingerprint = fingerprint
	ctx = sourcegate.WithLease(ctx, lease)
	if chat.SourceBindingRevision == r.BeforeBindingRevision && chat.ConnectionRevision == r.BeforeConnectionRevision {
		_ = s.access.chats.MarkPendingCAS(ctx, chat, &pending)
		chat, err = s.access.chats.ReadCurrent(ctx, req.OwnerID, req.ChatID)
		if err != nil {
			return nil, err
		}
	}
	if chat.SourceID != source.SourceID || chat.SourceBindingRevision != r.AfterBindingRevision || chat.ConnectionRevision != pending.ConnectionRevision || chat.SourceConnectionFingerprint != fingerprint {
		return nil, ErrERPRegistrationConflict
	}
	binding := models.ERPChatBinding{ChatID: req.ChatID, OwnerID: req.OwnerID, ConnectionFingerprint: fingerprint, BindingRevision: r.AfterBindingRevision, OperationID: r.BindingOperationID}
	old := sourceBinding(source, req.ChatID)
	if old == nil || old.BindingRevision == r.BeforeBindingRevision {
		_ = s.access.repo.PutBindingCAS(ctx, source.SourceID, source.ConfigRevision, old, binding)
		source, err = s.access.source(ctx, source.SourceID)
		if err != nil {
			return nil, err
		}
		old = sourceBinding(source, req.ChatID)
	}
	if old == nil || old.ChatID != binding.ChatID || old.OwnerID != binding.OwnerID || old.BindingRevision != binding.BindingRevision || old.OperationID != binding.OperationID || old.ConnectionFingerprint != binding.ConnectionFingerprint {
		return nil, ErrERPRegistrationConflict
	}
	if !chat.SourceBindingConfirmed {
		final := *chat
		final.SourceBindingConfirmed = true
		final.Connection = chat.Connection.Clone()
		final.Connection.CurrentSchema, final.Connection.SchemaUpdatedAt = nil, nil
		_ = s.access.chats.CommitBindingCAS(ctx, chat, &final)
		chat, err = s.access.chats.ReadCurrent(ctx, req.OwnerID, req.ChatID)
		if err != nil {
			return nil, err
		}
		if !sameChatBinding(&final, chat) {
			return nil, ErrERPRegistrationConflict
		}
	}
	if !old.Confirmed {
		_ = s.access.repo.ConfirmBindingCAS(ctx, source.SourceID, source.ConfigRevision, binding)
		source, err = s.access.source(ctx, source.SourceID)
		if err != nil {
			return nil, err
		}
	}
	if s.access.binding(source, req.OwnerID, chat, fingerprint) != nil {
		return nil, ErrERPRegistrationConflict
	}
	metadata, ok := s.access.chats.(interface {
		PatchMetadata(context.Context, primitive.ObjectID, primitive.ObjectID, repositories.ChatMetadataPatch) error
	})
	if !ok {
		return nil, ErrERPRegistrationInput
	}
	name := "yanfa-erp"
	if metadata.PatchMetadata(ctx, req.OwnerID, req.ChatID, repositories.ChatMetadataPatch{DisplayName: &name}) != nil {
		return nil, ErrERPRegistrationConflict
	}
	return source, nil
}

func (s *ERPSourceRegistrationService) EnableSync(ctx context.Context, req ERPRegistrationEnableRequest) (*ERPRegistrationOutput, error) {
	if !models.CanonicalSyncUUID(req.RequestID) || !models.CanonicalSyncUUID(req.SourceID) || req.OperatorApprovalRef == "" || req.LegacyWriterDisabledRef == "" || req.StageARecoveryEvidenceRef == "" || req.ApplicationReadEvidenceRef == "" || req.TrustedInstallationEvidenceRef == "" || len(req.SyncOperatorAccountIDs) == 0 {
		return nil, ErrERPRegistrationInput
	}
	for _, key := range []string{"Q15-01", "Q15-02", "Q15-03", "Q15-04", "G15-0"} {
		if req.ApprovalRefs[key] == "" {
			return nil, ErrERPRegistrationInput
		}
	}
	if req.Runtime.G15ApprovalRef != req.ApprovalRefs["G15-0"] {
		return nil, ErrERPRegistrationInput
	}
	loaded, publicHash, loadErr := LoadERPRegistrationConfig(req.PublicConfigPath)
	if loadErr != nil || !reflect.DeepEqual(loaded, s.cfg) {
		return nil, ErrERPRegistrationConflict
	}
	instance, err := s.offline()
	if err != nil {
		return nil, err
	}
	defer instance.Close()
	source, err := s.access.source(ctx, req.SourceID)
	if err != nil {
		return nil, err
	}
	if len(source.ShopScope) != 7 || len(source.OrderSourceByShop) != 7 || !safeSyncToken(req.Runtime.AftersaleSource, 128) {
		return nil, ErrERPRegistrationInput
	}
	seenShops := make(map[string]bool, 7)
	for _, shop := range source.ShopScope {
		if !safeSyncDigits(shop) || seenShops[shop] || !safeSyncToken(source.OrderSourceByShop[shop], 128) {
			return nil, ErrERPRegistrationInput
		}
		seenShops[shop] = true
	}
	r := source.RegistrationReceipt
	if r == nil || r.RequestID != req.StageViewRequestID || r.Phase == "view_pending" || r.AfterBindingRevision != req.ExpectedBindingRevision || r.PublicConfigCandidateSHA256 != req.PublicConfigBeforeSHA256 {
		return nil, ErrERPRegistrationConflict
	}
	if publicHash != req.PublicConfigBeforeSHA256 && publicHash != r.EnableCandidateSHA256 {
		return nil, ErrERPRegistrationConflict
	}
	for _, owner := range req.SyncOperatorAccountIDs {
		if owner.IsZero() || !accountAllowed(source.ViewAccountIDs, owner) {
			return nil, ErrERPRegistrationInput
		}
	}
	chat, err := s.access.chats.ReadCurrent(ctx, r.OwnerID, r.ExistingChatID)
	if err != nil || s.access.binding(source, r.OwnerID, chat, chatFingerprint(chat)) != nil {
		return nil, ErrERPRegistrationConflict
	}
	digest := registrationDigest(req)
	if r.EnableRequestID != "" && (r.EnableRequestID != req.RequestID || r.EnableInputSHA256 != digest) {
		return nil, ErrERPRegistrationConflict
	}
	identity := sourcegate.Identity{SourceID: source.SourceID, OperationID: r.BindingOperationID, ConfigRevision: source.ConfigRevision}
	lease, err := s.access.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, identity)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	if s.confirmOffline(ctx, source, lease, req.Runtime) != nil {
		return nil, ErrSourceRecovering
	}
	if r.Phase == "sync_enabled" {
		next := *source
		next.ConfigRevision = r.EnableBeforeConfigRevision + 1
		next.SyncOperatorAccountIDs = req.SyncOperatorAccountIDs
		next.ApprovalRefs = req.ApprovalRefs
		next.ComponentManifestRef = req.Runtime.ManifestRef
		next.SecretInjectionRef = req.Runtime.PrivateEnvironment.Reference
		if !sameRegistrationConfig(source, &next) {
			return nil, ErrERPRegistrationConflict
		}
		candidate, e := s.candidate(source, req.Runtime)
		if e != nil {
			return nil, e
		}
		hash := syncDigest(registrationConfigBytes(candidate))
		if hash != r.EnableCandidateSHA256 {
			return nil, ErrERPRegistrationConflict
		}
		return &ERPRegistrationOutput{SourceID: source.SourceID, PublicConfigBeforeSHA256: req.PublicConfigBeforeSHA256, PublicConfigSHA256: hash, PublicConfig: candidate, Receipt: r}, nil
	}
	if r.EnableRequestID == "" {
		if source.ConfigRevision != req.ExpectedConfigRevision || source.StateRevision != req.ExpectedStateRevision || source.Phase != models.ERPSourceIdle || source.RecoveryVerifiedInstanceID == "" {
			return nil, ErrERPRegistrationConflict
		}
		next := *r
		next.Revision++
		next.EnableRequestID = req.RequestID
		next.EnableInputSHA256 = digest
		next.EnableBeforeConfigRevision = source.ConfigRevision
		next.EnableBeforeStateRevision = source.StateRevision
		next.EnablePhase = "intent"
		source, err = s.saveReceipt(ctx, source, &next)
		if err != nil {
			return nil, err
		}
		r = source.RegistrationReceipt
	}
	if source.StateRevision == r.EnableBeforeStateRevision {
		_ = s.access.repo.UpdateStateCAS(ctx, source.SourceID, source.StateRevision, "", repositories.ERPSourceState{Phase: models.ERPSourceRecovering, LastReleasedOperationID: source.LastReleasedOperationID})
		source, err = s.access.source(ctx, source.SourceID)
		if err != nil {
			return nil, err
		}
	}
	if source.StateRevision != r.EnableBeforeStateRevision+1 || source.Phase != models.ERPSourceRecovering || source.ActiveOperation != nil || source.RecoveryVerifiedInstanceID != "" {
		return nil, ErrERPRegistrationConflict
	}
	next := *source
	next.ConfigRevision = r.EnableBeforeConfigRevision + 1
	next.SyncOperatorAccountIDs = req.SyncOperatorAccountIDs
	next.ApprovalRefs = req.ApprovalRefs
	next.ComponentManifestRef = req.Runtime.ManifestRef
	next.SecretInjectionRef = req.Runtime.PrivateEnvironment.Reference
	if source.ConfigRevision == r.EnableBeforeConfigRevision {
		_ = s.access.repo.UpdateConfigCAS(ctx, source.SourceID, source.ConfigRevision, &next)
		source, err = s.access.source(ctx, source.SourceID)
		if err != nil {
			return nil, err
		}
	}
	if !sameRegistrationConfig(source, &next) {
		return nil, ErrERPRegistrationConflict
	}
	candidate, err := s.candidate(source, req.Runtime)
	if err != nil {
		return nil, err
	}
	hash := syncDigest(registrationConfigBytes(candidate))
	receipt := *source.RegistrationReceipt
	if receipt.EnableCandidateSHA256 != "" && receipt.EnableCandidateSHA256 != hash {
		return nil, ErrERPRegistrationConflict
	}
	if receipt.Phase != "sync_enabled" {
		receipt.Revision++
		receipt.Phase = "sync_enabled"
		receipt.EnablePhase = "config_observed"
		receipt.EnableCandidateSHA256 = hash
		source, err = s.saveReceipt(ctx, source, &receipt)
		if err != nil {
			return nil, err
		}
	}
	return &ERPRegistrationOutput{SourceID: source.SourceID, PublicConfigBeforeSHA256: req.PublicConfigBeforeSHA256, PublicConfigSHA256: hash, PublicConfig: candidate, Receipt: source.RegistrationReceipt}, nil
}
func chatFingerprint(chat *models.Chat) string {
	if chat == nil {
		return ""
	}
	plain := chat.Connection.Clone()
	utils.DecryptConnection(&plain)
	return ConnectionFingerprint(plain)
}

// WriteERPRegistrationOutput never publishes configuration. Same bytes may be
// regenerated after lost output; different existing bytes are a conflict.
func WriteERPRegistrationOutput(directory, stage string, output *ERPRegistrationOutput) error {
	if stage != "stage-view" && stage != "enable-sync" || output == nil {
		return ErrERPRegistrationInput
	}
	if err := secureRegistrationDirectory(directory); err != nil {
		return err
	}
	files := map[string][]byte{stage + "-config.json": registrationConfigBytes(output.PublicConfig)}
	raw, _ := json.MarshalIndent(output, "", "  ")
	files[stage+"-receipt.json"] = append(raw, '\n')
	for name, raw := range files {
		if err := writeRegistrationFile(filepath.Join(directory, name), raw); err != nil {
			return err
		}
	}
	return nil
}
func secureRegistrationDirectory(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return ErrERPRegistrationInput
	}
	// Reuse the immutable/private ancestor walk via a sentinel opened below;
	// directory traversal itself must also reject links and writable ancestors.
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrERPRegistrationInput
	}
	defer func() { unix.Close(fd) }()
	for _, part := range splitRegistrationPath(directory) {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return ErrERPRegistrationInput
		}
		unix.Close(fd)
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&0022 != 0 || st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
			return ErrERPRegistrationInput
		}
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		return ErrERPRegistrationInput
	}
	return nil
}
func splitRegistrationPath(path string) []string {
	var parts []string
	for path != "/" {
		parts = append(parts, filepath.Base(path))
		path = filepath.Dir(path)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}
func writeRegistrationFile(path string, raw []byte) error {
	if f, e := openSyncFile(path, uint32(os.Geteuid()), 0600, false); e == nil {
		defer f.Close()
		old, e := io.ReadAll(io.LimitReader(f, int64(len(raw)+1)))
		if e != nil || !bytes.Equal(old, raw) {
			return ErrERPRegistrationConflict
		}
		return nil
	}
	temporary := path + "." + uuid.NewString() + ".tmp"
	if erpsyncinstall.WriteExclusive(temporary, raw, 0600) != nil {
		return ErrERPRegistrationInput
	}
	defer os.Remove(temporary)
	// link is an exclusive publication: a conflicting target is never replaced.
	if err := os.Link(temporary, path); err != nil {
		return ErrERPRegistrationConflict
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrERPRegistrationInput
	}
	defer dir.Close()
	return dir.Sync()
}
