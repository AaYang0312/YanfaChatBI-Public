package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/dbmanager"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"yanfachatbi/internal/utils"
)

const (
	assistantMetricVersion         = "assistant-metrics-v1"
	assistantReportMessageText     = "经营报告生成中"
	assistantReportNotCopiedNotice = "经营报告未复制，请在原对话中查看。"
)

// AssistantService is the only HTTP-facing assistant entry. AccessContext is
// always server-built; none of these methods read owner from a request body.
type AssistantService interface {
	Resolve(ctx context.Context, access models.AccessContext, draft models.DraftRequest) (models.Resolution, error)
	CreateRun(ctx context.Context, access models.AccessContext, req models.AnalysisRequest) (models.RunView, error)
	GetRun(ctx context.Context, access models.AccessContext, runID string) (models.RunView, error)
	CancelRun(ctx context.Context, access models.AccessContext, runID string) (models.RunView, error)
}

// AssistantExecutor runs one persisted run asynchronously. Execute is an
// internal entry: it reloads the stored run and refuses to start a row that
// is not still created for that owner and chat.
type AssistantExecutor interface {
	Execute(ctx context.Context, runID string) error
}

// AssistantRunNotFoundError is the uniform not-found result for a missing or
// foreign chat and a missing or foreign run. Callers map it to HTTP 404 and
// must not distinguish the two.
type AssistantRunNotFoundError struct{}

func (AssistantRunNotFoundError) Error() string { return "assistant resource not found" }

// IsAssistantNotFound reports whether err is the uniform 404 result.
func IsAssistantNotFound(err error) bool {
	var missing AssistantRunNotFoundError
	return errors.As(err, &missing)
}

// errReportUnavailable is returned when assistant report decryption fails.
// Public GetRun/CancelRun callers receive this safe message instead of the
// underlying cipher error.
var errReportUnavailable = errors.New("assistant report is unavailable")

// assistantRunLocator reloads one persisted run for the asynchronous executor.
// The service records owner and chat when it creates the run, so Execute does
// not scan the collection and does not authorize from the run ID alone.
type assistantRunLocator interface {
	Remember(run models.RunRecord)
	Locate(runID string) (ownerID, chatID string, ok bool)
}

type assistantRunMemory struct {
	mu   sync.Mutex
	rows map[string]assistantRunKey
}

type assistantRunKey struct {
	ownerID string
	chatID  string
}

func newAssistantRunMemory() *assistantRunMemory {
	return &assistantRunMemory{rows: map[string]assistantRunKey{}}
}

func (m *assistantRunMemory) Remember(run models.RunRecord) {
	if m == nil || run.RunID == "" || run.OwnerID == "" || run.ChatID == "" {
		return
	}
	m.mu.Lock()
	m.rows[run.RunID] = assistantRunKey{ownerID: run.OwnerID, chatID: run.ChatID}
	m.mu.Unlock()
}

func (m *assistantRunMemory) Locate(runID string) (string, string, bool) {
	if m == nil {
		return "", "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.rows[runID]
	if !ok || key.ownerID == "" || key.chatID == "" {
		return "", "", false
	}
	return key.ownerID, key.chatID, true
}

// AssistantChatLookup is the ownership lookup the assistant service performs
// before any source call. FindOwnedChat returns the chat only when it belongs
// to ownerID; a missing or foreign chat is the same not-found result.
type AssistantChatLookup interface {
	FindOwnedChat(ctx context.Context, ownerID, chatID string) (*models.Chat, error)
}

// AssistantMessageStore persists the report message and, when implemented,
// reloads it so GetRun can reject a dangling run reference. Lookup is
// optional: stores that only insert do not change the owner/chat read path.
type AssistantMessageStore interface {
	InsertReportMessage(ctx context.Context, message *models.Message) error
}

// AssistantMessageLookup loads one stored message by ID. GetRun uses it
// after owner and chat already match. A missing lookup keeps the previous
// read path for stores that cannot see chat messages.
type AssistantMessageLookup interface {
	FindReportMessage(ctx context.Context, messageID string) (*models.Message, bool, error)
}

// AssistantProductCatalog resolves product query text to candidates. It is not
// a fact read: implementations must not call ReadSalesFacts or ReadRefundFacts.
type AssistantProductCatalog interface {
	FindProductCandidates(ctx context.Context, access models.AccessContext, productQuery string) ([]models.Candidate, error)
}

// AssistantServiceOption configures optional service dependencies.
type AssistantServiceOption func(*assistantService)

// WithAssistantClock overrides the clock used for default windows and timestamps.
func WithAssistantClock(now func() time.Time) AssistantServiceOption {
	return func(s *assistantService) {
		if now != nil {
			s.now = now
		}
	}
}

// WithAssistantInstanceID sets the executing instance stored on new runs.
func WithAssistantInstanceID(instanceID string) AssistantServiceOption {
	return func(s *assistantService) {
		if instanceID != "" {
			s.instanceID = instanceID
		}
	}
}

// WithAssistantRunIDGenerator overrides run ID allocation. Tests use it to
// keep identifiers stable.
func WithAssistantRunIDGenerator(next func() string) AssistantServiceOption {
	return func(s *assistantService) {
		if next != nil {
			s.newRunID = next
		}
	}
}

// WithAssistantExplanationModel attaches the explanation model used to generate
// metric narratives. A nil model keeps explanation unavailable, which is the
// default for unconfigured deployments.
func WithAssistantExplanationModel(model AssistantExplanationModel) AssistantServiceOption {
	return func(s *assistantService) {
		s.explanationModel = model
	}
}

// WithAssistantDemoIsolation limits the fixture to an explicitly enabled
// independent demo database. Other sources need an approved metric contract.
func WithAssistantDemoIsolation(enabled bool) AssistantServiceOption {
	return func(s *assistantService) {
		s.enforceDemoIsolation = true
		s.demoEnabled = enabled
	}
}

// WithAssistantERPQuantityPilot enables the payment quantity contract only for
// registered, read-admitted ERP sources with a frozen seven-shop mapping.
func WithAssistantERPQuantityPilot(sourceID, scopeSHA256 string) AssistantServiceOption {
	return func(s *assistantService) {
		digest, err := hex.DecodeString(scopeSHA256)
		if models.CanonicalSyncUUID(sourceID) && err == nil && len(digest) == sha256.Size {
			s.erpQuantitySourceID = sourceID
			s.erpQuantityScopeSHA256 = hex.EncodeToString(digest)
		}
	}
}

type assistantService struct {
	chats                AssistantChatLookup
	messages             AssistantMessageStore
	runs                 repositories.AssistantRunRepository
	cipher               repositories.ReportCipher
	catalog              AssistantProductCatalog
	source               repositories.AssistantSourceRepository
	executor             AssistantExecutor
	located              assistantRunLocator
	now                  func() time.Time
	instanceID           string
	newRunID             func() string
	sourceAccess         *SourceAccessService
	enforceDemoIsolation bool
	demoEnabled          bool

	erpQuantitySourceID    string
	erpQuantityScopeSHA256 string

	explanationModel AssistantExplanationModel

	mu       sync.Mutex
	started  map[string]struct{}
	cancels  map[string]context.CancelFunc
	notify   AssistantRunNotifier
	orphaned bool
}

// NewAssistantService wires the assistant HTTP service. source may be nil only
// when no caller will resolve or execute; a later source call then fails closed.
// executor may be nil at construction and attached with SetExecutor once the
// executor, which depends on this service, exists.
func NewAssistantService(
	chats AssistantChatLookup,
	messages AssistantMessageStore,
	runs repositories.AssistantRunRepository,
	cipher repositories.ReportCipher,
	catalog AssistantProductCatalog,
	source repositories.AssistantSourceRepository,
	executor AssistantExecutor,
	sourceAccess *SourceAccessService,
	opts ...AssistantServiceOption,
) AssistantService {
	if sourceAccess == nil {
		panic("source access policy is required")
	}
	svc := &assistantService{
		sourceAccess: sourceAccess,
		chats:        chats,
		messages:     messages,
		runs:         runs,
		cipher:       cipher,
		catalog:      catalog,
		source:       source,
		executor:     executor,
		located:      newAssistantRunMemory(),
		now:          time.Now,
		instanceID:   "assistant",
		newRunID: func() string {
			return primitive.NewObjectID().Hex()
		},
		started: map[string]struct{}{},
		cancels: map[string]context.CancelFunc{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	if svc.executor == nil {
		svc.executor = svc
	}
	svc.interruptOrphans(context.Background())
	return svc
}

// AssistantRunNotifier publishes a status notification. It is not the run
// record: a send error is ignored and never cancels the run. Tests use it to
// prove a dropped SSE frame leaves the persisted status unchanged.
type AssistantRunNotifier interface {
	PublishRunUpdate(ctx context.Context, view models.RunView) error
}

// WithAssistantRunNotifier attaches an optional status publisher. A nil
// notifier disables publication. SSE failure must not cancel a run.
func WithAssistantRunNotifier(notify AssistantRunNotifier) AssistantServiceOption {
	return func(s *assistantService) {
		s.notify = notify
	}
}

// SetExecutor attaches the asynchronous executor after both values exist.
// Calling it twice replaces the executor; in-flight runs keep the one they started with.
func (s *assistantService) SetExecutor(executor AssistantExecutor) {
	if s == nil {
		return
	}
	s.executor = executor
}

// Resolve returns resolved or needs_input. The first repository call is the
// chat ownership lookup. Ambiguous product text lists every matching candidate
// and does not read facts.
func (s *assistantService) Resolve(ctx context.Context, access models.AccessContext, draft models.DraftRequest) (models.Resolution, error) {
	if err := s.requireOwnedChat(ctx, &access); err != nil {
		return models.Resolution{}, err
	}
	read, err := s.acquireSource(ctx, access)
	if err != nil {
		return models.Resolution{}, err
	}
	defer read.Close()
	return s.resolveAdmitted(read.Context, access, draft, read)
}

func (s *assistantService) resolveAdmitted(ctx context.Context, access models.AccessContext, draft models.DraftRequest, read *SourceRead) (models.Resolution, error) {
	catalog := s.catalog
	catalogAccess := access
	if s.useERPQuantityPilot(read) {
		if len(draft.Metrics) == 0 {
			return ResolveDraft(access, draft, s.now(), nil)
		}
		pilotDraft := models.AnalysisRequest{
			Basis: draft.Basis, Period: assistantDraftPeriod(draft.Period), Metrics: draft.Metrics,
			Product: assistantDraftProduct(draft.Product), LowMarginThreshold: draft.LowMarginThreshold,
		}
		if err := validateAssistantERPQuantityPilot(pilotDraft); err != nil {
			return models.Resolution{}, err
		}
		if draft.Scope != nil {
			scope, missing, err := assistantValidateScope(*draft.Scope)
			if err != nil {
				return models.Resolution{}, err
			}
			if len(missing) > 0 {
				return ResolveDraft(access, draft, s.now(), nil)
			}
			if err := AuthorizeScope(access, scope); err != nil {
				return models.Resolution{}, err
			}
			if err := checkAssistantERPPilotShops(scope, read.Source.OrderSourceByShop); err != nil {
				return models.Resolution{}, err
			}
			if scope.Mode == models.ScopeModeSelected {
				catalogAccess.ApprovedScope = models.ApprovedScope{ShopIDs: scope.ShopIDs, PlatformIDs: scope.PlatformIDs}
			}
		}
		source, closeReader, err := s.openERPQuantitySource(ctx, access, read)
		if err != nil {
			return models.Resolution{}, err
		}
		defer closeReader()
		catalog = source
	}
	candidates, err := s.productCandidates(ctx, catalogAccess, draft, catalog)
	if err != nil {
		return models.Resolution{}, err
	}
	resolution, err := ResolveDraft(access, draft, s.now(), candidates)
	if err != nil {
		return models.Resolution{}, err
	}
	if err := ValidateResolution(resolution); err != nil {
		return models.Resolution{}, err
	}
	return resolution, nil
}

// CreateRun validates a resolved request, persists the run and its message
// reference, and only then starts asynchronous execution. A failed message
// insert transitions the run to failed and does not start the executor.
// Replaying the same request returns the original run; the same request ID
// with a different fingerprint returns request_conflict.
func (s *assistantService) CreateRun(ctx context.Context, access models.AccessContext, req models.AnalysisRequest) (models.RunView, error) {
	if err := s.requireOwnedChat(ctx, &access); err != nil {
		return models.RunView{}, err
	}
	read, err := s.acquireSource(ctx, access)
	if err != nil {
		return models.RunView{}, err
	}
	defer read.Close()
	ctx = read.Context
	normalized, err := ValidateAnalysisRequest(access, req, s.now())
	if err != nil {
		return models.RunView{}, err
	}
	if s.useERPQuantityPilot(read) {
		if err := validateAssistantERPQuantityPilot(normalized); err != nil {
			return models.RunView{}, err
		}
		if err := checkAssistantERPPilotShops(normalized.Scope, read.Source.OrderSourceByShop); err != nil {
			return models.RunView{}, err
		}
	}
	fingerprint, err := assistantRequestFingerprint(normalized)
	if err != nil {
		return models.RunView{}, err
	}
	messageID := primitive.NewObjectID()
	now := s.now().UTC()
	record := models.RunRecord{
		SchemaVersion:      models.AssistantSchemaVersion,
		RunID:              s.newRunID(),
		OwnerID:            access.OwnerID,
		ChatID:             access.ChatID,
		RequestID:          normalized.RequestID,
		RequestFingerprint: fingerprint,
		NormalizedInput:    normalized,
		MessageID:          messageID.Hex(),
		Status:             models.RunStatusCreated,
		Stage:              models.RunStageCreated,
		CreatedAt:          now,
		UpdatedAt:          now,
		ExecutingInstance:  s.instanceID,
		Version: models.RunVersion{
			SchemaVersion: models.AssistantSchemaVersion,
			MetricVersion: assistantMetricVersion,
		},
	}
	stored, err := s.runs.CreateOrGet(ctx, record)
	if err != nil {
		return models.RunView{}, err
	}
	s.located.Remember(stored)
	if stored.RunID != record.RunID {
		return s.viewFromRecord(stored)
	}
	if err := s.persistReportMessage(ctx, access, stored, messageID); err != nil {
		s.failBeforeExecution(ctx, stored, err)
		return models.RunView{}, err
	}
	if err := s.startExecution(ctx, stored, read); err != nil {
		s.failBeforeExecution(ctx, stored, err)
		return models.RunView{}, err
	}
	return s.viewFromRecord(stored)
}

// GetRun returns the persisted run for this owner and chat. It does not read
// the source and does not recompute metrics. When the message store can look
// messages up, the stored run.message_id must still point at a message in
// this chat whose assistant_run_id is this run. A missing or mismatched
// message is 404. The run document is left unchanged.
func (s *assistantService) GetRun(ctx context.Context, access models.AccessContext, runID string) (models.RunView, error) {
	if err := s.requireOwnedChat(ctx, &access); err != nil {
		return models.RunView{}, err
	}
	record, err := s.runs.GetOwned(ctx, access.OwnerID, access.ChatID, strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, repositories.ErrRunNotFound) {
			return models.RunView{}, AssistantRunNotFoundError{}
		}
		return models.RunView{}, err
	}
	if err := s.requireReferencedMessage(ctx, access, record); err != nil {
		return models.RunView{}, err
	}
	return s.viewFromRecord(record)
}

// requireReferencedMessage checks run.message_id against the chat message
// store. Stores without FindReportMessage skip the check. A found message
// must belong to this chat and carry this run id. The run row is not updated.
func (s *assistantService) requireReferencedMessage(ctx context.Context, access models.AccessContext, record models.RunRecord) error {
	lookup, ok := s.messages.(AssistantMessageLookup)
	if !ok || lookup == nil {
		return nil
	}
	message, found, err := lookup.FindReportMessage(ctx, record.MessageID)
	if err != nil {
		return err
	}
	if !found || message == nil {
		return AssistantRunNotFoundError{}
	}
	if message.ChatID.Hex() != access.ChatID {
		return AssistantRunNotFoundError{}
	}
	if message.AssistantRunID == nil || strings.TrimSpace(*message.AssistantRunID) != record.RunID {
		return AssistantRunNotFoundError{}
	}
	return nil
}

// CancelRun checks owner, chat, and run before it touches a context. The
// cancel func is keyed by run ID, never by stream ID, so two runs that share
// a stream do not cancel each other. A terminal run is returned unchanged.
// Once cancellation is intended, the run context is cancelled before the
// status is judged, so Execute cannot finish a completed report in the window.
// A lost created-to-cancelled compare-and-swap retries while the row is still
// created or running. The returned view is always terminal: cancelled, or the
// other terminal state that won the race.
func (s *assistantService) CancelRun(ctx context.Context, access models.AccessContext, runID string) (models.RunView, error) {
	if err := s.requireOwnedChat(ctx, &access); err != nil {
		return models.RunView{}, err
	}
	runID = strings.TrimSpace(runID)
	record, err := s.runs.GetOwned(ctx, access.OwnerID, access.ChatID, runID)
	if err != nil {
		if errors.Is(err, repositories.ErrRunNotFound) {
			return models.RunView{}, AssistantRunNotFoundError{}
		}
		return models.RunView{}, err
	}
	if record.OwnerID != access.OwnerID || record.ChatID != access.ChatID || record.RunID != runID {
		return models.RunView{}, AssistantRunNotFoundError{}
	}
	if record.Status.IsTerminal() {
		return s.viewFromRecord(record)
	}
	s.cancelRunContext(record.RunID)
	marked := true
	current := record
	for {
		updated, err := s.runs.Transition(ctx, models.RunTransition{
			OwnerID:            current.OwnerID,
			ChatID:             current.ChatID,
			RunID:              current.RunID,
			From:               current.Status,
			To:                 models.RunStatusCancelled,
			ExpectedRevision:   current.Revision,
			Stage:              models.RunStageCancelled,
			CancellationMarked: &marked,
			UpdatedAt:          s.now().UTC(),
		})
		if err == nil {
			return s.viewFromRecord(updated)
		}
		if !errors.Is(err, repositories.ErrTransitionConflict) {
			return models.RunView{}, err
		}
		reloaded, getErr := s.runs.GetOwned(ctx, access.OwnerID, access.ChatID, record.RunID)
		if getErr != nil {
			return models.RunView{}, err
		}
		if reloaded.Status.IsTerminal() {
			return s.viewFromRecord(reloaded)
		}
		current = reloaded
	}
}

// Execute reloads the run by its stored owner and chat, moves created to
// running with one conditional update, then runs the existing workflow once.
// A replay that finds the run already past created returns without a second start.
func (s *assistantService) Execute(ctx context.Context, runID string) error {
	if s == nil || s.runs == nil {
		return errors.New("assistant executor is not configured")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return repositories.ErrRunNotFound
	}
	record, err := s.loadLocatedRun(ctx, runID)
	if err != nil {
		return err
	}
	if record.Status != models.RunStatusCreated {
		return nil
	}
	read, err := s.acquireSource(ctx, models.AccessContext{OwnerID: record.OwnerID, ChatID: record.ChatID})
	if err != nil {
		return err
	}
	defer read.Close()
	ctx = read.Context
	running, err := s.runs.Transition(ctx, models.RunTransition{
		OwnerID:           record.OwnerID,
		ChatID:            record.ChatID,
		RunID:             record.RunID,
		From:              models.RunStatusCreated,
		To:                models.RunStatusRunning,
		ExpectedRevision:  record.Revision,
		Stage:             models.RunStageRunning,
		ExecutingInstance: s.instanceID,
		UpdatedAt:         s.now().UTC(),
	})
	if errors.Is(err, repositories.ErrTransitionConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil || running.CancellationMarked {
		return s.cancelRunning(context.Background(), running)
	}
	report, execErr := s.executeAdmittedWorkflow(ctx, running, read)
	if execErr != nil {
		if errors.Is(execErr, context.Canceled) || ctx.Err() != nil {
			return s.cancelRunning(context.Background(), running)
		}
		_, _ = s.failRun(ctx, running, execErr)
		return execErr
	}
	if ctx.Err() != nil {
		return s.cancelRunning(context.Background(), running)
	}
	current, err := s.loadLocatedRun(ctx, running.RunID)
	if err != nil {
		return err
	}
	if current.Status != models.RunStatusRunning || current.CancellationMarked {
		if current.Status.IsTerminal() {
			return nil
		}
		return s.cancelRunning(context.Background(), current)
	}
	running = current
	plain, err := assistantReportBytes(report)
	if err != nil {
		_, _ = s.failRun(ctx, running, err)
		return err
	}
	completed, err := s.runs.Transition(ctx, models.RunTransition{
		OwnerID:          running.OwnerID,
		ChatID:           running.ChatID,
		RunID:            running.RunID,
		From:             models.RunStatusRunning,
		To:               models.RunStatusCompleted,
		ExpectedRevision: running.Revision,
		Stage:            models.RunStageCompleted,
		ReportPlaintext:  plain,
		Version:          &running.Version,
		UpdatedAt:        s.now().UTC(),
	})
	if errors.Is(err, repositories.ErrTransitionConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	s.publishRunUpdate(ctx, completed)
	return nil
}

func (s *assistantService) cancelRunning(ctx context.Context, run models.RunRecord) error {
	if run.Status != models.RunStatusRunning {
		return nil
	}
	marked := true
	_, err := s.runs.Transition(ctx, models.RunTransition{
		OwnerID:            run.OwnerID,
		ChatID:             run.ChatID,
		RunID:              run.RunID,
		From:               models.RunStatusRunning,
		To:                 models.RunStatusCancelled,
		ExpectedRevision:   run.Revision,
		Stage:              models.RunStageCancelled,
		CancellationMarked: &marked,
		UpdatedAt:          s.now().UTC(),
	})
	if errors.Is(err, repositories.ErrTransitionConflict) {
		current, getErr := s.runs.GetOwned(ctx, run.OwnerID, run.ChatID, run.RunID)
		if getErr != nil {
			return err
		}
		if current.Status.IsTerminal() {
			return nil
		}
		return err
	}
	return err
}

func (s *assistantService) requireOwnedChat(ctx context.Context, access *models.AccessContext) error {
	if access == nil || strings.TrimSpace(access.OwnerID) == "" || strings.TrimSpace(access.ChatID) == "" {
		return AssistantRunNotFoundError{}
	}
	if s == nil || s.chats == nil {
		return errors.New("assistant chat lookup is not configured")
	}
	chat, err := s.chats.FindOwnedChat(ctx, access.OwnerID, access.ChatID)
	if err != nil {
		if errors.Is(err, repositories.ErrRunNotFound) || errors.Is(err, mongo.ErrNoDocuments) {
			return AssistantRunNotFoundError{}
		}
		var missing AssistantRunNotFoundError
		if errors.As(err, &missing) {
			return missing
		}
		return err
	}
	if chat == nil || chat.UserID.Hex() != access.OwnerID || chat.ID.Hex() != access.ChatID {
		return AssistantRunNotFoundError{}
	}
	access.ConnectionID = chat.Connection.ID.Hex()
	if access.ApprovedScope.Empty() {
		access.ApprovedScope = models.ApprovedScope{All: true}
	}
	return nil
}

func (s *assistantService) acquireSource(ctx context.Context, access models.AccessContext) (*SourceRead, error) {
	chat, err := s.chats.FindOwnedChat(ctx, access.OwnerID, access.ChatID)
	if err != nil {
		return nil, err
	}
	if chat == nil || chat.UserID.Hex() != access.OwnerID || chat.ID.Hex() != access.ChatID {
		return nil, AssistantRunNotFoundError{}
	}
	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	read, err := s.sourceAccess.AcquireRead(ctx, chat.UserID, chat, connection)
	if err != nil {
		return nil, err
	}
	if s.enforceDemoIsolation && !s.useERPQuantityPilot(read) && !s.isDemoConnection(read, connection) {
		_ = read.Close()
		return nil, models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "source.contract_version",
			"this data source does not yet have an approved real-data workflow contract")
	}
	return read, nil
}

func (s *assistantService) productCandidates(ctx context.Context, access models.AccessContext, draft models.DraftRequest, catalog AssistantProductCatalog) ([]models.Candidate, error) {
	if draft.Workflow != "" && draft.Workflow != models.WorkflowProductAnalysis {
		return nil, nil
	}
	if draft.Product != nil && strings.TrimSpace(draft.Product.ProductID) != "" {
		return nil, nil
	}
	if strings.TrimSpace(draft.ProductQuery) == "" {
		return nil, nil
	}
	if catalog == nil {
		return nil, nil
	}
	return catalog.FindProductCandidates(ctx, access, draft.ProductQuery)
}

func (s *assistantService) persistReportMessage(ctx context.Context, access models.AccessContext, run models.RunRecord, messageID primitive.ObjectID) error {
	if s.messages == nil {
		return errors.New("assistant message store is not configured")
	}
	ownerID, err := primitive.ObjectIDFromHex(access.OwnerID)
	if err != nil {
		return AssistantRunNotFoundError{}
	}
	chatID, err := primitive.ObjectIDFromHex(access.ChatID)
	if err != nil {
		return AssistantRunNotFoundError{}
	}
	runID := run.RunID
	now := s.now().UTC()
	message := &models.Message{
		UserID:         ownerID,
		ChatID:         chatID,
		Type:           string(constants.MessageTypeAssistant),
		Content:        assistantReportMessageText,
		Queries:        nil,
		AssistantRunID: &runID,
		Base: models.Base{
			ID:        messageID,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	return s.messages.InsertReportMessage(ctx, message)
}

func (s *assistantService) failBeforeExecution(ctx context.Context, run models.RunRecord, cause error) {
	_, _ = s.runs.Transition(ctx, models.RunTransition{
		OwnerID:          run.OwnerID,
		ChatID:           run.ChatID,
		RunID:            run.RunID,
		From:             models.RunStatusCreated,
		To:               models.RunStatusFailed,
		ExpectedRevision: run.Revision,
		Stage:            models.RunStageFailed,
		SafeError:        assistantSafeError(cause),
		UpdatedAt:        s.now().UTC(),
	})
}

func (s *assistantService) startExecution(ctx context.Context, run models.RunRecord, read *SourceRead) error {
	if s.executor == nil || run.Status != models.RunStatusCreated {
		return nil
	}
	runCtx, release, err := read.retain(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	s.mu.Lock()
	if _, exists := s.started[run.RunID]; exists {
		s.mu.Unlock()
		release()
		return nil
	}
	s.started[run.RunID] = struct{}{}
	runCtx, cancel := context.WithCancel(runCtx)
	s.cancels[run.RunID] = cancel
	s.mu.Unlock()
	go func() {
		defer release()
		defer cancel()
		defer s.clearRunContext(run.RunID)
		if err := s.executor.Execute(runCtx, run.RunID); err != nil {
			if sourceAccessCause(err) == nil {
				log.Printf("Assistant run %s failure=execution_failed", run.RunID)
				return
			}
			// Only this authorized launch may fail its original created
			// revision. Cancellation can win CAS without cancelling cleanup.
			terminalCtx := context.WithoutCancel(runCtx)
			failed, persistErr := s.failRun(terminalCtx, run, err)
			if errors.Is(persistErr, repositories.ErrTransitionConflict) {
				return
			}
			if persistErr != nil {
				log.Printf("Assistant run %s failure=source_refusal_persistence_failed", run.RunID)
				return
			}
			s.publishRunUpdate(terminalCtx, failed)
		}
	}()
	return nil
}

// cancelRunContext cancels only the context registered for this run ID.
// A shared stream ID is not a key in this map.
func (s *assistantService) cancelRunContext(runID string) {
	if s == nil || runID == "" {
		return
	}
	s.mu.Lock()
	cancel := s.cancels[runID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *assistantService) clearRunContext(runID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.started, runID)
	delete(s.cancels, runID)
	s.mu.Unlock()
}

// interruptOrphans runs once, from service startup, with the current process
// ID. A repository error does not fail construction. The method never reads a source.
func (s *assistantService) interruptOrphans(ctx context.Context) {
	if s == nil || s.runs == nil || s.instanceID == "" {
		return
	}
	s.mu.Lock()
	if s.orphaned {
		s.mu.Unlock()
		return
	}
	s.orphaned = true
	s.mu.Unlock()
	_, _ = s.runs.InterruptOrphans(ctx, s.instanceID)
}

// publishRunUpdate emits one status notification after a persisted transition.
// The notifier error is discarded: losing the frame does not cancel the run
// and does not rewrite its status.
func (s *assistantService) publishRunUpdate(ctx context.Context, record models.RunRecord) {
	if s == nil || s.notify == nil {
		return
	}
	view, err := s.viewFromRecord(record)
	if err != nil {
		return
	}
	_ = s.notify.PublishRunUpdate(ctx, view)
}

func (s *assistantService) executeWorkflow(ctx context.Context, run models.RunRecord) (models.AnalysisReport, error) {
	if s.source == nil {
		return models.AnalysisReport{}, errors.New("assistant source is not configured")
	}
	workflow, err := NewAssistantWorkflow(run.NormalizedInput, s.source, s.explanationModel,
		WithWorkflowChat(run.ChatID, run.OwnerID))
	if err != nil {
		return models.AnalysisReport{}, err
	}
	return workflow.Invoke(ctx, models.AnalysisReport{ReportID: run.RunID})
}

func assistantDraftPeriod(period *models.Period) models.Period {
	if period == nil {
		return models.Period{}
	}
	return *period
}

func assistantDraftProduct(product *models.ProductSelection) models.ProductSelection {
	if product == nil {
		return models.ProductSelection{}
	}
	return *product
}

func (s *assistantService) isDemoConnection(read *SourceRead, connection models.Connection) bool {
	return s.demoEnabled && read.Classification == models.SourceIndependent && !connection.SSHEnabled &&
		connection.Type == "postgresql" && connection.Host == "yanfachatbi-spreadsheet-postgres" &&
		connection.Database == "yanfa_bi_demo" && connection.Port != nil && *connection.Port == "5432" &&
		connection.Username != nil && *connection.Username == "yanfa_demo_reader"
}

func (s *assistantService) useERPQuantityPilot(read *SourceRead) bool {
	if s.erpQuantitySourceID == "" || read.Classification != models.SourceRegisteredERP ||
		read.Source == nil || read.Source.SourceID != s.erpQuantitySourceID ||
		len(read.Source.ShopScope) != 7 || len(read.Source.OrderSourceByShop) != 7 {
		return false
	}
	seen := make(map[string]bool, 7)
	for _, shop := range read.Source.ShopScope {
		if shop == "" || seen[shop] || read.Source.OrderSourceByShop[shop] == "" {
			return false
		}
		seen[shop] = true
	}
	return assistantERPScopeDigest(read.Source.OrderSourceByShop) == s.erpQuantityScopeSHA256
}

func assistantERPScopeDigest(orderSourceByShop map[string]string) string {
	// JSON sorts string map keys; hashing the key and value protects the
	// authorized shop set and each shop's source together.
	encoded, _ := json.Marshal(orderSourceByShop)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateAssistantERPQuantityPilot(req models.AnalysisRequest) error {
	if (req.Basis != "" && req.Basis != models.BasisPayment) ||
		(req.Period.Timezone != "" && req.Period.Timezone != models.AssistantDefaultTimezone) ||
		len(req.Metrics) != 1 || req.Metrics[0] != models.MetricQuantity ||
		len(req.Product.SkuIDs) != 0 || req.LowMarginThreshold != nil {
		return models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "metrics",
			"ERP pilot supports only payment-basis ordinary-sale quantity by product, in Asia/Shanghai")
	}
	return nil
}

func checkAssistantERPPilotShops(scope models.Scope, approved map[string]string) error {
	for _, shop := range scope.ShopIDs {
		if approved[shop] == "" {
			return models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope.shop_ids",
				"requested shop is outside the covered ERP pilot scope")
		}
	}
	return nil
}

func (s *assistantService) openERPQuantitySource(ctx context.Context, access models.AccessContext, read *SourceRead) (*AssistantERPQuantitySource, func(), error) {
	chat, err := s.chats.FindOwnedChat(ctx, access.OwnerID, access.ChatID)
	if err != nil {
		return nil, nil, err
	}
	if chat == nil || chat.UserID.Hex() != access.OwnerID || chat.ID.Hex() != access.ChatID {
		return nil, nil, AssistantRunNotFoundError{}
	}
	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	conf := connectionConfig(chat, connection)
	conf.ApplicationName = s.sourceAccess.applicationName(chat)
	reader, err := dbmanager.NewSourceAssistantReadonlyReader(ctx, conf, s.sourceAccess)
	if err != nil {
		if cause := sourceAccessCause(err); cause != nil {
			return nil, nil, cause
		}
		return nil, nil, models.NewValidationError(models.ErrorCodeSourceUnavailable, "source", "could not open approved ERP reader")
	}
	source, err := NewAssistantERPQuantitySource(reader, read.Source.OrderSourceByShop)
	if err != nil {
		_ = reader.Close()
		return nil, nil, err
	}
	return source, func() { _ = reader.Close() }, nil
}

func (s *assistantService) executeAdmittedWorkflow(ctx context.Context, run models.RunRecord, read *SourceRead) (models.AnalysisReport, error) {
	if !s.useERPQuantityPilot(read) {
		return s.executeWorkflow(ctx, run)
	}
	if err := validateAssistantERPQuantityPilot(run.NormalizedInput); err != nil {
		return models.AnalysisReport{}, err
	}
	source, closeReader, err := s.openERPQuantitySource(ctx, models.AccessContext{OwnerID: run.OwnerID, ChatID: run.ChatID}, read)
	if err != nil {
		return models.AnalysisReport{}, err
	}
	defer closeReader()
	// Real pilot reports are deterministic. Explanations can be enabled only
	// after the deployment's outbound model policy has been approved.
	workflow, err := NewAssistantWorkflow(run.NormalizedInput, source, nil)
	if err != nil {
		return models.AnalysisReport{}, err
	}
	return workflow.Invoke(ctx, models.AnalysisReport{ReportID: run.RunID})
}

func (s *assistantService) failRun(ctx context.Context, run models.RunRecord, cause error) (models.RunRecord, error) {
	return s.runs.Transition(ctx, models.RunTransition{
		OwnerID:          run.OwnerID,
		ChatID:           run.ChatID,
		RunID:            run.RunID,
		From:             run.Status,
		To:               models.RunStatusFailed,
		ExpectedRevision: run.Revision,
		Stage:            models.RunStageFailed,
		SafeError:        assistantSafeError(cause),
		UpdatedAt:        s.now().UTC(),
	})
}

func (s *assistantService) viewFromRecord(record models.RunRecord) (models.RunView, error) {
	view := models.RunViewFromRecord(record)
	if len(record.EncryptedResult) == 0 {
		return view, nil
	}
	report, err := repositories.DecryptReport(s.cipher, record)
	if err != nil {
		return models.RunView{}, errReportUnavailable
	}
	view.Report = report
	return view, nil
}

// loadLocatedRun reloads a run the service itself created. The run ID is not
// an authorization key: GetOwned still requires the stored owner and chat.
func (s *assistantService) loadLocatedRun(ctx context.Context, runID string) (models.RunRecord, error) {
	if s == nil || s.located == nil || s.runs == nil {
		return models.RunRecord{}, errors.New("assistant run locator is not configured")
	}
	ownerID, chatID, ok := s.located.Locate(runID)
	if !ok {
		return models.RunRecord{}, repositories.ErrRunNotFound
	}
	record, err := s.runs.GetOwned(ctx, ownerID, chatID, runID)
	if err != nil {
		return models.RunRecord{}, err
	}
	if record.OwnerID != ownerID || record.ChatID != chatID || record.RunID != runID {
		return models.RunRecord{}, repositories.ErrRunNotFound
	}
	return record, nil
}

func assistantRequestFingerprint(req models.AnalysisRequest) (string, error) {
	canonical := req
	canonical.StreamID = ""
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("fingerprint assistant request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// assistantReportBytes uses the report's JSON contract, including exact decimal
// strings and required arrays. BSON encoding is only for the run document.
func assistantReportBytes(report models.AnalysisReport) ([]byte, error) {
	plain, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("encode assistant report: %w", err)
	}
	if len(plain) == 0 {
		return nil, errors.New("assistant report plaintext is empty")
	}
	return plain, nil
}

func assistantSafeError(cause error) *models.SafeRunError {
	if sourceCause := sourceAccessCause(cause); sourceCause != nil {
		return &models.SafeRunError{Code: sourceCause.Error(), Message: "data source access was refused"}
	}
	if validation, ok := models.AsValidationError(cause); ok {
		return &models.SafeRunError{Code: string(validation.Code), Message: validation.Message}
	}
	return &models.SafeRunError{Code: "failed", Message: "assistant run failed before a report was stored"}
}

// AssistantChatRepository adapts the existing chat repository. FindOwnedChat is
// the ownership lookup and compares chat.UserID with the caller. It does not
// call verifyQueryOwnership.
type AssistantChatRepository struct {
	chats repositories.ChatRepository
}

// NewAssistantChatRepository wraps the existing chat repository.
func NewAssistantChatRepository(chats repositories.ChatRepository) AssistantChatLookup {
	return &AssistantChatRepository{chats: chats}
}

// FindOwnedChat loads the chat and returns it only when chat.UserID matches
// ownerID. A missing chat and a foreign chat are the same not-found result.
func (r *AssistantChatRepository) FindOwnedChat(_ context.Context, ownerID, chatID string) (*models.Chat, error) {
	if r == nil || r.chats == nil {
		return nil, errors.New("assistant chat repository is not configured")
	}
	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, AssistantRunNotFoundError{}
	}
	ownerObjID, err := primitive.ObjectIDFromHex(ownerID)
	if err != nil {
		return nil, AssistantRunNotFoundError{}
	}
	chat, err := r.chats.FindByID(chatObjID)
	if err != nil {
		return nil, err
	}
	if chat == nil || chat.UserID != ownerObjID {
		return nil, AssistantRunNotFoundError{}
	}
	return chat, nil
}

// AssistantMessageRepository adapts chat message inserts. The assistant path
// does not call ChatService.CreateMessage, so generic message ownership stays unchanged.
type AssistantMessageRepository struct {
	chats repositories.ChatRepository
}

// NewAssistantMessageRepository wraps the existing chat repository.
func NewAssistantMessageRepository(chats repositories.ChatRepository) AssistantMessageStore {
	return &AssistantMessageRepository{chats: chats}
}

// InsertReportMessage stores one report message. Queries stay nil.
func (r *AssistantMessageRepository) InsertReportMessage(_ context.Context, message *models.Message) error {
	if r == nil || r.chats == nil {
		return errors.New("assistant message repository is not configured")
	}
	if message == nil {
		return errors.New("assistant report message is nil")
	}
	message.Queries = nil
	return r.chats.CreateMessage(message)
}

// FindReportMessage loads run.message_id through ChatRepository.FindMessageByID.
// found is false when the id is blank or the message is gone. Other lookup
// errors are returned. This method does not update or delete the run.
func (r *AssistantMessageRepository) FindReportMessage(_ context.Context, messageID string) (*models.Message, bool, error) {
	if r == nil || r.chats == nil {
		return nil, false, errors.New("assistant message repository is not configured")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, false, nil
	}
	objectID, err := primitive.ObjectIDFromHex(messageID)
	if err != nil {
		return nil, false, nil
	}
	message, err := r.chats.FindMessageByID(objectID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if message == nil || message.ID.IsZero() {
		return nil, false, nil
	}
	return message, true, nil
}

// AssistantFixtureCatalog resolves product text against the frozen fixture
// identity only. It does not read sales or refund facts.
type AssistantFixtureCatalog struct{}

// NewAssistantFixtureCatalog returns the fixture product catalog.
func NewAssistantFixtureCatalog() AssistantProductCatalog {
	return AssistantFixtureCatalog{}
}

// FindProductCandidates returns fixture products whose label contains the query.
// The fixture has one product, so a query contained by that label returns one
// candidate. Callers that need the two-candidate ambiguity case supply their own catalog.
func (AssistantFixtureCatalog) FindProductCandidates(_ context.Context, _ models.AccessContext, productQuery string) ([]models.Candidate, error) {
	query := strings.TrimSpace(productQuery)
	if query == "" {
		return nil, nil
	}
	if strings.Contains(strings.ToLower(AssistantFixtureProduct), strings.ToLower(query)) ||
		strings.Contains(strings.ToLower(query), strings.ToLower(AssistantFixtureProduct)) {
		return []models.Candidate{{
			EntityType: models.EntityTypeProduct,
			EntityID:   AssistantFixtureProductID,
			Label:      AssistantFixtureProduct,
			PlatformID: AssistantFixturePlatformID,
		}}, nil
	}
	return nil, nil
}
