package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/sourcegate"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	ErrSourceUnresolved       = errors.New("ERP_SOURCE_UNRESOLVED")
	ErrSourceForbidden        = errors.New("ERP_SOURCE_FORBIDDEN")
	ErrSourceRecovering       = errors.New("DATA_SYNC_RECOVERING")
	ErrSourceSyncing          = errors.New("DATA_SYNC_IN_PROGRESS")
	ErrSourceStateUnavailable = errors.New("ERP_SOURCE_STATE_UNAVAILABLE")
	ErrSourceBindingConflict  = repositories.ErrChatBindingConflict
)

// ConnectionFingerprint consumes a decrypted connection. It hashes no passwords,
// tokens, private keys or secret references. It is not an authorization decision.
func ConnectionFingerprint(connection models.Connection) string {
	return connectionIdentity(connection, true)
}

// EndpointFingerprint classifies approved physical endpoints even when a new
// Chat changes its reader or driver. Full binding validation still uses the
// reader/driver-sensitive ConnectionFingerprint before any source operation.
func EndpointFingerprint(connection models.Connection) string {
	return connectionIdentity(connection, false)
}

func connectionIdentity(connection models.Connection, includeReader bool) string {
	value := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	host := func(s string) string {
		s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
		if ip := net.ParseIP(strings.Trim(s, "[]")); ip != nil {
			return ip.String()
		}
		return s
	}
	port := value(connection.Port)
	if p, err := strconv.ParseUint(port, 10, 16); err == nil {
		port = strconv.FormatUint(p, 10)
	}
	if port == "" {
		switch connection.Type {
		case "postgresql", "timescaledb":
			port = "5432"
		case "yugabytedb":
			port = "5433"
		case "mysql":
			port = "3306"
		case "starrocks":
			port = "9030"
		case "clickhouse":
			port = "9000"
		case "mongodb":
			port = "27017"
		}
	}
	sshHost, sshPort, sshUser := "", "", ""
	if connection.SSHEnabled {
		sshHost, sshPort, sshUser = host(value(connection.SSHHost)), value(connection.SSHPort), value(connection.SSHUsername)
		if sshPort == "" {
			sshPort = "22"
		}
	}
	driver, reader, authDatabase := strings.ToLower(connection.Type), value(connection.Username), value(connection.AuthDatabase)
	if !includeReader {
		driver, reader, authDatabase, sshUser = "", "", "", ""
	}
	encoded, _ := json.Marshal(struct {
		Driver, Host, Port, Database, Reader, AuthDatabase string
		SSHEnabled                                         bool
		SSHHost, SSHPort, SSHReader                        string
	}{driver, host(connection.Host), port, connection.Database, reader,
		authDatabase, connection.SSHEnabled, sshHost, sshPort, sshUser})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// SourceAccessService owns trusted identity and admission across application roots.
// ERP execution also requires the deployment's verified recovery identity.
type SourceAccessService struct {
	repo          repositories.ERPSourceRepository
	gate          *sourcegate.Gate
	instanceID    string
	uid           uint32
	lockDirectory string
	associations  map[string]string // immutable approved identity directory, never busy state
	chats         BindingChatStore
	syncRuntime   config.ERPSyncRuntime
}

func NewSourceAccessService(repo repositories.ERPSourceRepository, gate *sourcegate.Gate, cfg config.ERPSyncConfig, chats BindingChatStore) (*SourceAccessService, error) {
	if repo == nil || gate == nil || chats == nil || cfg.InstanceID == "" || !filepath.IsAbs(cfg.LockDirectory) || gate.Directory() != cfg.LockDirectory || gate.UID() != cfg.DeploymentUID {
		return nil, ErrSourceUnresolved
	}
	directory := make(map[string]string)
	for _, association := range cfg.Associations {
		if association.ApprovalRef == "" {
			return nil, ErrSourceUnresolved
		}
		if association.SourceID != "" {
			id, err := uuid.Parse(association.SourceID)
			if err != nil || id == uuid.Nil || id.String() != association.SourceID {
				return nil, ErrSourceUnresolved
			}
		}
		for _, fingerprint := range association.EndpointFingerprints {
			if len(fingerprint) != sha256.Size*2 {
				return nil, ErrSourceUnresolved
			}
			if _, err := hex.DecodeString(fingerprint); err != nil {
				return nil, ErrSourceUnresolved
			}
			if _, exists := directory[fingerprint]; exists {
				return nil, ErrSourceBindingConflict
			}
			directory[fingerprint] = association.SourceID
		}
	}
	return &SourceAccessService{repo: repo, gate: gate, instanceID: cfg.InstanceID, uid: cfg.DeploymentUID,
		lockDirectory: cfg.LockDirectory, associations: directory, chats: chats, syncRuntime: cfg.Sync}, nil
}

// Before deployment activation there is no approved ERP catalog. Sticky ERP
// records still fail closed; unrelated databases need no state-store dependency.
func NewUnconfiguredSourceAccessService(chats BindingChatStore) *SourceAccessService {
	return &SourceAccessService{associations: map[string]string{}, chats: chats}
}

func (s *SourceAccessService) applicationName(chat *models.Chat) string {
	instance := sha256.Sum256([]byte(s.instanceID))
	return "yanfa:" + chat.SourceID + ":" + hex.EncodeToString(instance[:10])
}

func (s *SourceAccessService) AcquireSource(ctx context.Context, chat *models.Chat, connection models.Connection) (context.Context, func(), error) {
	read, err := s.AcquireRead(ctx, chat.UserID, chat, connection)
	if err != nil {
		return nil, nil, err
	}
	return read.Context, func() { read.Close() }, nil
}

// RegisterApproved provisions a new server UUID only after explicit evidence.
// It always begins recovering, with no bindings or operator privileges. This is
// an internal deployment operation; no HTTP/DI caller is enabled by this task.
func (s *SourceAccessService) RegisterApproved(ctx context.Context, registration models.ERPSource) (*models.ERPSource, error) {
	prepared, err := s.prepareRegistration(registration)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (s *SourceAccessService) prepareRegistration(registration models.ERPSource) (*models.ERPSource, error) {
	if registration.ApprovalRefs["Q15-02"] == "" || registration.SourceID != "" || len(registration.ChatBindings) != 0 || len(registration.SyncOperatorAccountIDs) != 0 || registration.RegistrationReceipt != nil {
		return nil, ErrSourceForbidden
	}
	registration.SourceID = uuid.NewString()
	registration.SchemaVersion, registration.ConfigRevision, registration.StateRevision = 1, 1, 0
	registration.DeploymentUID = s.uid
	registration.LockPath = filepath.Join(s.lockDirectory, registration.SourceID+".lock")
	registration.Phase, registration.ActiveOperation = models.ERPSourceRecovering, nil
	registration.RecoveryVerifiedInstanceID, registration.LastReleasedOperationID = "", ""
	target, err := s.gate.Provision(registration.SourceID)
	if err != nil {
		return nil, err
	}
	registration.LockDevice, registration.LockInode = target.Device, target.Inode
	return &registration, nil
}

func (s *SourceAccessService) Classify(chat *models.Chat, connection models.Connection) (models.SourceClassification, string) {
	id, associated := s.associations[EndpointFingerprint(connection)]
	if chat != nil && (chat.ERPAssociated || chat.SourceID != "" || chat.SourceBindingRevision != 0) {
		if !associated || id == "" || (chat.SourceID != "" && chat.SourceID != id) {
			return models.SourceUnresolvedERP, id
		}
		return models.SourceRegisteredERP, id
	}
	if !associated {
		return models.SourceIndependent, ""
	}
	if id == "" {
		return models.SourceUnresolvedERP, ""
	}
	return models.SourceRegisteredERP, id
}

func sourceBinding(source *models.ERPSource, chatID primitive.ObjectID) *models.ERPChatBinding {
	for i := range source.ChatBindings {
		if source.ChatBindings[i].ChatID == chatID {
			return &source.ChatBindings[i]
		}
	}
	return nil
}

func accountAllowed(accounts []primitive.ObjectID, owner primitive.ObjectID) bool {
	for _, id := range accounts {
		if id == owner {
			return true
		}
	}
	return false
}

func (s *SourceAccessService) source(ctx context.Context, id string) (*models.ERPSource, error) {
	if s.repo == nil || s.gate == nil {
		return nil, ErrSourceUnresolved
	}
	source, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, ErrSourceStateUnavailable
	}
	if source.DeploymentUID != s.uid || source.LockPath != filepath.Join(s.lockDirectory, id+".lock") {
		return nil, ErrSourceUnresolved
	}
	return source, nil
}

func (s *SourceAccessService) ready(source *models.ERPSource) error {
	if source.RegistrationReceipt != nil && source.RegistrationReceipt.Phase == "view_pending" {
		return ErrSourceRecovering
	}
	if source.Phase == models.ERPSourceSyncing {
		return ErrSourceSyncing
	}
	if source.Phase != models.ERPSourceIdle || source.ActiveOperation != nil || source.RecoveryVerifiedInstanceID != s.instanceID {
		return ErrSourceRecovering
	}
	return nil
}

func (s *SourceAccessService) binding(source *models.ERPSource, owner primitive.ObjectID, chat *models.Chat, fingerprint string) error {
	if chat == nil || chat.UserID != owner || !accountAllowed(source.ViewAccountIDs, owner) {
		return ErrSourceForbidden
	}
	if !chat.ERPAssociated || !chat.SourceBindingConfirmed || chat.SourceID != source.SourceID || chat.SourceBindingRevision < 1 || chat.SourceConnectionFingerprint != fingerprint {
		return ErrSourceUnresolved
	}
	binding := sourceBinding(source, chat.ID)
	if binding == nil || !binding.Confirmed || binding.OwnerID != owner || binding.BindingRevision != chat.SourceBindingRevision || binding.ConnectionFingerprint != fingerprint {
		return ErrSourceUnresolved
	}
	for _, fingerprintApproved := range source.ApprovedConnectionFingerprints {
		if fingerprintApproved == fingerprint {
			return nil
		}
	}
	return ErrSourceUnresolved
}

func sourceTarget(source *models.ERPSource) sourcegate.Target {
	return sourcegate.Target{SourceID: source.SourceID, Device: source.LockDevice, Inode: source.LockInode}
}

func readIdentity(source *models.ERPSource, owner primitive.ObjectID, chat *models.Chat, fingerprint, operationID string) sourcegate.Identity {
	return sourcegate.Identity{SourceID: source.SourceID, OperationID: operationID, OwnerID: owner.Hex(), ChatID: chat.ID.Hex(),
		BindingRevision: chat.SourceBindingRevision, ConnectionFingerprint: fingerprint, ConfigRevision: source.ConfigRevision}
}

type SourceRead struct {
	Classification models.SourceClassification
	Context        context.Context
	Lease          *sourcegate.Lease // nil only for a genuinely independent source
	Source         *models.ERPSource
}

func (r *SourceRead) Close() error {
	if r == nil {
		return nil
	}
	return r.Lease.Close()
}

// retain transfers a reference before a detached descendant is launched.
func (r *SourceRead) retain(ctx context.Context) (context.Context, func(), error) {
	if r.Classification == models.SourceIndependent {
		return ctx, func() {}, nil
	}
	if r.Lease == nil {
		return nil, nil, sourcegate.ErrMissing
	}
	child, err := r.Lease.Retain()
	if err != nil {
		return nil, nil, err
	}
	return sourcegate.WithLease(ctx, child), func() { child.Close() }, nil
}

func ownedSourceChat(ctx context.Context, chats repositories.ChatRepository, ownerID, chatID string) (*models.Chat, error) {
	owner, err := primitive.ObjectIDFromHex(ownerID)
	if err != nil {
		return nil, ErrSourceForbidden
	}
	id, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, ErrSourceForbidden
	}
	chat, err := chats.FindByID(id)
	if err != nil {
		return nil, err
	}
	if chat == nil || chat.UserID != owner {
		return nil, ErrSourceForbidden
	}
	return chat, nil
}

func acquireChatSource(ctx context.Context, chats repositories.ChatRepository, policy *SourceAccessService, ownerID, chatID string) (*models.Chat, *SourceRead, error) {
	chat, err := ownedSourceChat(ctx, chats, ownerID, chatID)
	if err != nil {
		return nil, nil, err
	}
	if policy == nil {
		return nil, nil, ErrSourceUnresolved
	}
	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	read, err := policy.AcquireRead(ctx, chat.UserID, chat, connection)
	return chat, read, err
}

var sourceAccessCauses = [...]error{
	ErrSourceForbidden,
	ErrSourceUnresolved,
	ErrSourceRecovering,
	ErrSourceSyncing,
	ErrSourceStateUnavailable,
	ErrSourceBindingConflict,
	sourcegate.ErrBusy,
	sourcegate.ErrMissing,
	sourcegate.ErrIdentity,
	sourcegate.ErrClosed,
}

// sourceAccessCause returns only the known admission sentinel, stripping any
// wrapping details before an asynchronous consumer persists or publishes it.
func sourceAccessCause(err error) error {
	for _, cause := range sourceAccessCauses[:] {
		if errors.Is(err, cause) {
			return cause
		}
	}
	return nil
}

// sourceAccessCauseFromCode accepts only a complete canonical code from a
// SOURCE_ACCESS_DENIED DTO. Unknown messages remain a safe denial.
func sourceAccessCauseFromCode(code string) error {
	for _, cause := range sourceAccessCauses[:] {
		if code == cause.Error() {
			return cause
		}
	}
	return ErrSourceUnresolved
}

// SourceAccessHTTPStatus keeps admission errors out of retry/model fallbacks.
func SourceAccessHTTPStatus(err error) (int, bool) {
	switch sourceAccessCause(err) {
	case ErrSourceForbidden:
		return http.StatusForbidden, true
	case nil:
		return 0, false
	default:
		return http.StatusLocked, true
	}
}

func sourceHTTPStatus(err error) uint32 {
	if status, ok := SourceAccessHTTPStatus(err); ok {
		return uint32(status)
	}
	return http.StatusInternalServerError
}

type sourceOriginKey struct{}
type sourceOrigin struct {
	owner              primitive.ObjectID
	chat               primitive.ObjectID
	source             string
	connectionRevision int64
	bindingRevision    int64
	fingerprint        string
}

func sourceOriginContext(ctx context.Context, chat *models.Chat, connection models.Connection) (context.Context, error) {
	if !chat.ERPAssociated && chat.SourceID == "" && (connection.Type == "spreadsheet" || connection.Type == "google_sheets") {
		return ctx, nil
	}
	if _, binding := ctx.Value(bindingPermitKey{}).(*bindingPermit); binding {
		return ctx, nil
	}
	origin := sourceOrigin{owner: chat.UserID, chat: chat.ID, source: chat.SourceID,
		connectionRevision: chat.ConnectionRevision, bindingRevision: chat.SourceBindingRevision, fingerprint: ConnectionFingerprint(connection)}
	if inherited, exists := ctx.Value(sourceOriginKey{}).(sourceOrigin); exists {
		if inherited != origin {
			return nil, ErrSourceBindingConflict
		}
		return ctx, nil
	}
	return context.WithValue(ctx, sourceOriginKey{}, origin), nil
}

// AcquireRead is also the ordinary probe/unchanged-binding refresh root. It
// checks ownership first, takes SH before the recovery/config recheck and never
// reads ERP storage at all for an unrelated independent connection.
func (s *SourceAccessService) AcquireRead(ctx context.Context, owner primitive.ObjectID, chat *models.Chat, connection models.Connection) (*SourceRead, error) {
	if chat == nil || chat.UserID != owner {
		return nil, ErrSourceForbidden
	}
	var originErr error
	ctx, originErr = sourceOriginContext(ctx, chat, connection)
	if originErr != nil {
		return nil, originErr
	}
	classification, sourceID := s.Classify(chat, connection)
	if classification == models.SourceIndependent {
		return &SourceRead{Classification: classification, Context: ctx}, nil
	}
	if classification == models.SourceUnresolvedERP {
		return nil, ErrSourceUnresolved
	}
	if permit, ok := ctx.Value(bindingPermitKey{}).(*bindingPermit); ok {
		return s.inheritBindingRead(ctx, permit, owner, chat, connection)
	}
	current, err := s.chats.ReadCurrent(ctx, owner, chat.ID)
	if err != nil {
		return nil, ErrSourceStateUnavailable
	}
	if !sameChatBinding(chat, current) {
		return nil, ErrSourceBindingConflict
	}
	source, err := s.source(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	fingerprint := ConnectionFingerprint(connection)
	if err := s.binding(source, owner, chat, fingerprint); err != nil {
		return nil, err
	}
	operationID := uuid.NewString()
	if inherited, err := sourcegate.ContextIdentity(ctx, sourceID); err == nil {
		identity := readIdentity(source, owner, chat, fingerprint, inherited.OperationID)
		lease, err := sourcegate.FromContext(ctx, identity)
		if err != nil {
			return nil, err
		}
		child, err := lease.Retain()
		if err != nil {
			return nil, err
		}
		if err := s.ready(source); err != nil {
			child.Close()
			return nil, err
		}
		return &SourceRead{Classification: classification, Context: sourcegate.WithLease(ctx, child), Lease: child, Source: source}, nil
	} else if !errors.Is(err, sourcegate.ErrMissing) {
		return nil, err
	}
	identity := readIdentity(source, owner, chat, fingerprint, operationID)
	lease, err := s.gate.Acquire(ctx, sourceTarget(source), sourcegate.Shared, identity)
	if err != nil {
		return nil, err
	}
	confirmed, err := s.source(ctx, sourceID)
	if err == nil && confirmed.ConfigRevision != source.ConfigRevision {
		err = ErrSourceBindingConflict
	}
	if err == nil {
		err = s.binding(confirmed, owner, chat, fingerprint)
	}
	if err == nil {
		err = s.ready(confirmed)
	}
	if err != nil {
		lease.Close()
		return nil, err
	}
	return &SourceRead{Classification: classification, Context: sourcegate.WithLease(ctx, lease), Lease: lease, Source: confirmed}, nil
}

// RequireRead is the low-level T38 boundary. It never opens a file or grants a
// missing lease. Owner, Chat, endpoint, revision and operation must all match.
func (s *SourceAccessService) RequireRead(ctx context.Context, owner primitive.ObjectID, chat *models.Chat, connection models.Connection) error {
	if chat == nil || chat.UserID != owner {
		return ErrSourceForbidden
	}
	if _, exists := ctx.Value(sourceOriginKey{}).(sourceOrigin); exists {
		if _, err := sourceOriginContext(ctx, chat, connection); err != nil {
			return err
		}
	}
	classification, sourceID := s.Classify(chat, connection)
	if classification == models.SourceIndependent {
		return nil
	}
	if classification == models.SourceUnresolvedERP {
		return ErrSourceUnresolved
	}
	if permit, ok := ctx.Value(bindingPermitKey{}).(*bindingPermit); ok {
		read, err := s.inheritBindingRead(ctx, permit, owner, chat, connection)
		if read != nil {
			read.Close()
		}
		return err
	}
	current, err := s.chats.ReadCurrent(ctx, owner, chat.ID)
	if err != nil {
		return ErrSourceStateUnavailable
	}
	if !sameChatBinding(chat, current) {
		return ErrSourceBindingConflict
	}
	identity, err := sourcegate.ContextIdentity(ctx, sourceID)
	if err != nil {
		return err
	}
	source, err := s.source(ctx, sourceID)
	if err != nil {
		return err
	}
	fingerprint := ConnectionFingerprint(connection)
	if err := s.binding(source, owner, chat, fingerprint); err != nil {
		return err
	}
	if err := s.ready(source); err != nil {
		return err
	}
	_, err = sourcegate.FromContext(ctx, readIdentity(source, owner, chat, fingerprint, identity.OperationID))
	return err
}

// CanOperate does not imply readiness, admission or a right to create a job.
// An empty allow-list and copying a Chat never produce operation permission.
func (s *SourceAccessService) CanOperate(source *models.ERPSource, owner primitive.ObjectID) bool {
	return source != nil && source.ApprovalRefs["Q15-02"] != "" && accountAllowed(source.ViewAccountIDs, owner) && accountAllowed(source.SyncOperatorAccountIDs, owner)
}

// BindingChatStore is implemented by the existing primary ChatRepository.
type BindingChatStore interface {
	ReadCurrent(context.Context, primitive.ObjectID, primitive.ObjectID) (*models.Chat, error)
	MarkPendingCAS(context.Context, *models.Chat, *models.Chat) error
	CommitBindingCAS(context.Context, *models.Chat, *models.Chat) error
}

// BindingEffects implementations must propagate the supplied context all the
// way to source IO. Refresh returns only after actual goroutines, source calls
// and associated model descendants finish, including after cancellation.
type BindingEffects interface {
	Probe(context.Context, models.Connection) error
	Invalidate(context.Context, *models.Chat) error
	Refresh(context.Context, *models.Chat) error
}

type BindingChange struct {
	done chan struct{}
	err  error
}

// Wait observes actual completion; request cancellation is not completion.
func (change *BindingChange) Wait() error { <-change.done; return change.err }

type bindingPermitKey struct{}
type bindingPermit struct {
	service  *SourceAccessService
	identity sourcegate.Identity
	before   *models.Chat
	after    *models.Chat
}

func (s *SourceAccessService) inheritBindingRead(ctx context.Context, permit *bindingPermit, owner primitive.ObjectID, chat *models.Chat, connection models.Connection) (*SourceRead, error) {
	if permit.service != s || permit.identity.OwnerID != owner.Hex() || permit.identity.ChatID != chat.ID.Hex() {
		return nil, sourcegate.ErrIdentity
	}
	fingerprint := ConnectionFingerprint(connection)
	allowed := false
	for _, candidate := range []*models.Chat{permit.before, permit.after} {
		if candidate != nil && candidate.UserID == owner && candidate.ID == chat.ID && candidate.SourceID == permit.identity.SourceID &&
			candidate.SourceBindingRevision == chat.SourceBindingRevision && candidate.SourceConnectionFingerprint == fingerprint {
			allowed = true
		}
	}
	if !allowed {
		return nil, sourcegate.ErrIdentity
	}
	lease, err := sourcegate.FromContext(ctx, permit.identity)
	if err != nil || lease.Mode() != sourcegate.Exclusive {
		return nil, sourcegate.ErrIdentity
	}
	source, err := s.source(ctx, permit.identity.SourceID)
	if err != nil {
		return nil, err
	}
	if source.ConfigRevision != permit.identity.ConfigRevision || !accountAllowed(source.ViewAccountIDs, owner) || source.ActiveOperation == nil ||
		source.ActiveOperation.OperationID != permit.identity.OperationID || source.ActiveOperation.Kind != "binding_change" {
		return nil, ErrSourceBindingConflict
	}
	child, err := lease.Retain()
	if err != nil {
		return nil, err
	}
	return &SourceRead{Classification: models.SourceRegisteredERP, Context: sourcegate.WithLease(ctx, child), Lease: child, Source: source}, nil
}

func sameChatBinding(a, b *models.Chat) bool {
	return a != nil && b != nil && a.ID == b.ID && a.UserID == b.UserID && a.SourceID == b.SourceID &&
		a.SourceBindingRevision == b.SourceBindingRevision && a.SourceConnectionFingerprint == b.SourceConnectionFingerprint &&
		a.ConnectionRevision == b.ConnectionRevision &&
		a.SourceBindingConfirmed == b.SourceBindingConfirmed && a.ERPAssociated == b.ERPAssociated
}

// ChangeBinding acquires every old/new EX in source-id order before revalidation.
// Probe, both persistence halves and cache invalidation use those same handles.
// The returned async completion owns retained EX handles before it is launched.
// Errors after MarkPendingCAS leave an unresolved Chat and a durable recovery
// veto; they never restore an optimistic executable half-binding.
func (s *SourceAccessService) ChangeBinding(ctx context.Context, owner primitive.ObjectID, previous, next *models.Chat, plaintext models.Connection, store BindingChatStore, effects BindingEffects) (*BindingChange, error) {
	if previous == nil || next == nil || store == nil || effects == nil || previous.UserID != owner || next.UserID != owner || previous.ID != next.ID {
		return nil, ErrSourceForbidden
	}
	beforeSnapshot, nextSnapshot := *previous, *next
	beforeSnapshot.Connection, nextSnapshot.Connection = previous.Connection.Clone(), next.Connection.Clone()
	plaintext = plaintext.Clone()
	previous, next = &beforeSnapshot, &nextSnapshot
	classification, newID := s.Classify(previous, plaintext)
	// A trusted directory may explicitly map a changed endpoint to another ERP.
	if mapped, ok := s.associations[EndpointFingerprint(plaintext)]; ok && mapped != "" {
		newID, classification = mapped, models.SourceRegisteredERP
	}
	if classification == models.SourceIndependent {
		return nil, ErrSourceUnresolved
	}
	ids := []string{}
	for _, id := range []string{previous.SourceID, newID} {
		if id != "" && (len(ids) == 0 || ids[0] != id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	operationID := uuid.NewString()
	leases := make(map[string]*sourcegate.Lease, len(ids))
	sources := make(map[string]*models.ERPSource, len(ids))
	async := false
	defer func() {
		for _, lease := range leases {
			lease.Close()
		}
		if !async {
			for _, lease := range leases {
				lease.Join()
			}
		}
	}()
	pending := *previous
	pending.ERPAssociated, pending.SourceBindingConfirmed = true, false
	pending.SourceBindingRevision = previous.SourceBindingRevision + 1
	pending.ConnectionRevision = previous.ConnectionRevision + 1
	pending.SourceID, pending.SourceConnectionFingerprint = newID, ConnectionFingerprint(plaintext)
	if pending.SourceID == "" {
		pending.SourceID = previous.SourceID
	}
	for _, id := range ids {
		source, err := s.source(ctx, id)
		if err != nil {
			return nil, err
		}
		identityChat := previous
		if id == newID {
			identityChat = &pending
		}
		identity := readIdentity(source, owner, identityChat, identityChat.SourceConnectionFingerprint, operationID)
		lease, err := s.gate.Acquire(ctx, sourceTarget(source), sourcegate.Exclusive, identity)
		if err != nil {
			return nil, err
		}
		leases[id], sources[id] = lease, source
	}
	mutationCtx := ctx
	for _, id := range ids {
		mutationCtx = sourcegate.WithLease(mutationCtx, leases[id])
	}
	current, err := store.ReadCurrent(mutationCtx, owner, previous.ID)
	if err != nil {
		return nil, err
	}
	if !sameChatBinding(previous, current) || previous.SelectionRevision != current.SelectionRevision {
		return nil, ErrSourceBindingConflict
	}
	for _, id := range ids {
		confirmed, err := s.source(ctx, id)
		if err != nil {
			return nil, err
		}
		if confirmed.ConfigRevision != sources[id].ConfigRevision {
			return nil, ErrSourceBindingConflict
		}
		if !accountAllowed(confirmed.ViewAccountIDs, owner) {
			return nil, ErrSourceForbidden
		}
		if err := s.ready(confirmed); err != nil {
			return nil, err
		}
		if id == previous.SourceID && previous.SourceBindingConfirmed {
			if err := s.binding(confirmed, owner, previous, previous.SourceConnectionFingerprint); err != nil {
				return nil, err
			}
		}
		sources[id] = confirmed
	}
	// Preserve every source's release witness until its previous sync is
	// published. All EX handles and identity checks precede this guard; no Chat
	// or source binding mutation may run if any predecessor is still uncertain.
	for _, id := range ids {
		if err := s.repo.ConfirmSyncPredecessors(ctx, sources[id]); err != nil {
			return nil, ErrSourceRecovering
		}
	}
	if err := store.MarkPendingCAS(mutationCtx, current, &pending); err != nil {
		return nil, err
	}
	if classification == models.SourceUnresolvedERP || newID == "" {
		return nil, ErrSourceUnresolved
	}
	newSource := sources[newID]
	aliasApproved := false
	for _, fingerprintApproved := range newSource.ApprovedConnectionFingerprints {
		if fingerprintApproved == pending.SourceConnectionFingerprint {
			aliasApproved = true
		}
	}
	if !aliasApproved {
		return nil, ErrSourceUnresolved
	}
	// Reserve durable recovery vetoes only after all nonblocking EX acquisitions.
	for _, id := range ids {
		source := sources[id]
		operation := &models.ERPSourceOperation{OperationID: operationID, Kind: "binding_change", OwnerID: owner, ChatID: previous.ID,
			ConfigRevision: source.ConfigRevision, ComponentManifestRef: source.ComponentManifestRef, InstanceID: s.instanceID}
		if err := s.repo.UpdateStateCAS(ctx, id, source.StateRevision, "", repositories.ERPSourceState{
			Phase: models.ERPSourceRecovering, ActiveOperation: operation, RecoveryVerifiedInstanceID: s.instanceID,
		}); err != nil {
			return nil, err
		}
		source.StateRevision++
		source.Phase, source.ActiveOperation = models.ERPSourceRecovering, operation
	}
	priorTarget := sourceBinding(newSource, previous.ID)
	if priorTarget != nil && priorTarget.OwnerID != owner {
		return nil, ErrSourceForbidden
	}
	nextBinding := models.ERPChatBinding{ChatID: previous.ID, OwnerID: owner, BindingRevision: pending.SourceBindingRevision,
		ConnectionFingerprint: pending.SourceConnectionFingerprint, OperationID: operationID}
	if err := s.repo.PutBindingCAS(ctx, newID, newSource.ConfigRevision, priorTarget, nextBinding); err != nil {
		return nil, err
	}
	final := *next
	final.SourceID, final.SourceBindingRevision = pending.SourceID, pending.SourceBindingRevision
	final.ConnectionRevision = pending.ConnectionRevision
	final.SourceConnectionFingerprint = pending.SourceConnectionFingerprint
	final.ERPAssociated, final.SourceBindingConfirmed = true, true
	final.Connection.CurrentSchema, final.Connection.SchemaUpdatedAt = nil, nil
	contexts := make(map[string]context.Context, len(ids))
	for _, id := range ids {
		permit := &bindingPermit{service: s, identity: leases[id].Identity(), before: previous, after: &final}
		contexts[id] = context.WithValue(sourcegate.WithLease(ctx, leases[id]), bindingPermitKey{}, permit)
	}
	if err := effects.Probe(contexts[newID], plaintext); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.CommitBindingCAS(contexts[newID], &pending, &final); err != nil {
		return nil, err
	}
	invalidateID := previous.SourceID
	if invalidateID == "" {
		invalidateID = newID
	}
	if err := effects.Invalidate(contexts[invalidateID], previous); err != nil {
		return nil, err
	}
	if err := s.repo.ConfirmBindingCAS(ctx, newID, newSource.ConfigRevision, nextBinding); err != nil {
		return nil, err
	}
	// Re-read both halves before considering the new binding consistent. A source
	// recovery veto remains present until refresh and all descendants have joined.
	storedChat, err := store.ReadCurrent(ctx, owner, final.ID)
	if err != nil {
		return nil, err
	}
	storedSource, err := s.source(ctx, newID)
	if err != nil {
		return nil, err
	}
	if !sameChatBinding(&final, storedChat) {
		return nil, ErrSourceBindingConflict
	}
	if err := s.binding(storedSource, owner, storedChat, final.SourceConnectionFingerprint); err != nil {
		return nil, err
	}
	retained := make(map[string]*sourcegate.Lease, len(ids))
	for _, id := range ids {
		child, err := leases[id].Retain()
		if err != nil {
			for _, acquired := range retained {
				acquired.Close()
			}
			return nil, err
		}
		retained[id] = child
		contexts[id] = sourcegate.WithLease(contexts[id], child)
	}
	change := &BindingChange{done: make(chan struct{})}
	async = true
	go func() {
		defer func() {
			for _, child := range retained {
				child.Close()
			}
			close(change.done)
		}()
		finishErr := effects.Refresh(contexts[newID], &final)
		for _, id := range ids {
			if err := retained[id].JoinDescendants(); err != nil && finishErr == nil {
				finishErr = err
			}
		}
		// Cancellation signals work; Refresh must still join its actual children.
		if finishErr == nil {
			finishErr = ctx.Err()
		}
		cleanupCtx := context.WithoutCancel(contexts[newID])
		if finishErr == nil && previous.SourceID != "" && previous.SourceID != newID {
			oldSource := sources[previous.SourceID]
			if oldBinding := sourceBinding(oldSource, previous.ID); oldBinding != nil {
				finishErr = s.repo.RemoveBindingCAS(cleanupCtx, previous.SourceID, oldSource.ConfigRevision, *oldBinding)
			}
		}
		if finishErr == nil {
			chatCheck, readErr := store.ReadCurrent(cleanupCtx, owner, final.ID)
			if readErr != nil {
				finishErr = readErr
			} else if !sameChatBinding(&final, chatCheck) {
				finishErr = ErrSourceBindingConflict
			}
			if finishErr == nil {
				sourceCheck, readErr := s.source(cleanupCtx, newID)
				if readErr != nil {
					finishErr = readErr
				} else {
					finishErr = s.binding(sourceCheck, owner, chatCheck, final.SourceConnectionFingerprint)
				}
			}
		}
		if finishErr == nil {
			for _, id := range ids {
				source := sources[id]
				if err := s.repo.UpdateStateCAS(cleanupCtx, id, source.StateRevision, operationID, repositories.ERPSourceState{
					Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: s.instanceID, LastReleasedOperationID: operationID,
				}); err != nil {
					finishErr = err
					break
				}
			}
		}
		change.err = finishErr
	}()
	return change, nil
}
