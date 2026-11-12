package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/utils"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const assistantRunsCollection = "assistant_runs"

// AssistantRunRepository persists assistant runs. CreateOrGet is unique on
// owner, chat, and request ID. Transition is one conditional document update;
// it does not use a multi-document transaction. InterruptOrphans rewrites only
// nonterminal rows owned by another process and never reads a data source.
type AssistantRunRepository interface {
	CreateOrGet(ctx context.Context, run models.RunRecord) (models.RunRecord, error)
	GetOwned(ctx context.Context, ownerID, chatID, runID string) (models.RunRecord, error)
	Transition(ctx context.Context, transition models.RunTransition) (models.RunRecord, error)
	InterruptOrphans(ctx context.Context, ownerInstanceID string) (int64, error)
}

// ReportCipher encrypts and decrypts a report payload. Nil crypto is a hard
// failure: plaintext is never stored and ciphertext is never returned as JSON.
type ReportCipher interface {
	EncryptReport(plain []byte) ([]byte, error)
	DecryptReport(ciphertext []byte) ([]byte, error)
}

// AESReportCipher adapts AESGCMCrypto. It is not encryptQueryResult: an empty
// cipher or an encryption error returns the error and writes nothing.
type AESReportCipher struct {
	crypto *utils.AESGCMCrypto
}

// NewAESReportCipher wraps a configured AES-GCM cipher. A nil crypto is rejected.
func NewAESReportCipher(crypto *utils.AESGCMCrypto) (*AESReportCipher, error) {
	if crypto == nil {
		return nil, errors.New("assistant report cipher is nil")
	}
	return &AESReportCipher{crypto: crypto}, nil
}

// EncryptReport encrypts report bytes. It never substitutes plaintext.
func (c *AESReportCipher) EncryptReport(plain []byte) ([]byte, error) {
	if c == nil || c.crypto == nil {
		return nil, errors.New("assistant report cipher is nil")
	}
	if len(plain) == 0 {
		return nil, errors.New("assistant report plaintext is empty")
	}
	ciphertext, err := c.crypto.EncryptBytes(plain)
	if err != nil {
		return nil, fmt.Errorf("encrypt assistant report: %w", err)
	}
	if len(ciphertext) == 0 {
		return nil, errors.New("assistant report ciphertext is empty")
	}
	return ciphertext, nil
}

// DecryptReport decrypts report bytes. Failure returns an error; the ciphertext
// is not decoded as JSON.
func (c *AESReportCipher) DecryptReport(ciphertext []byte) ([]byte, error) {
	if c == nil || c.crypto == nil {
		return nil, errors.New("assistant report cipher is nil")
	}
	if len(ciphertext) == 0 {
		return nil, errors.New("assistant report ciphertext is empty")
	}
	plain, err := c.crypto.DecryptBytes(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt assistant report: %w", err)
	}
	return plain, nil
}

// AssistantRunCollection is the subset of mongo.Collection used by the run
// repository. Tests supply a fake; production supplies *mongo.Collection.
type AssistantRunCollection interface {
	InsertOne(ctx context.Context, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error)
	FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult
	FindOneAndUpdate(ctx context.Context, filter interface{}, update interface{}, opts ...*options.FindOneAndUpdateOptions) *mongo.SingleResult
	UpdateMany(ctx context.Context, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error)
	Indexes() mongo.IndexView
}

// ErrRunNotFound is returned when no document matches owner, chat, and run.
var ErrRunNotFound = errors.New("assistant run not found")

// ErrTransitionConflict is a failed conditional update: the expected status
// and revision no longer match, or the row is already terminal.
var ErrTransitionConflict = errors.New("assistant run transition conflict")

type assistantRunRepository struct {
	runs   AssistantRunCollection
	cipher ReportCipher
	now    func() time.Time
}

// NewAssistantRunRepository stores runs in the assistant_runs collection.
// cipher may be nil only when no caller will persist a report; any report
// write then fails closed before the collection is touched.
func NewAssistantRunRepository(runs AssistantRunCollection, cipher ReportCipher) AssistantRunRepository {
	return &assistantRunRepository{
		runs:   runs,
		cipher: cipher,
		now:    time.Now,
	}
}

// EnsureAssistantRunIndexes creates the owner/chat/request uniqueness index and
// the owner/chat/run lookup index. Callers that do not own index management
// can skip this; CreateOrGet still detects a duplicate key when the index exists.
func EnsureAssistantRunIndexes(ctx context.Context, runs AssistantRunCollection) error {
	if runs == nil {
		return errors.New("assistant run collection is nil")
	}
	_, err := runs.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "owner_id", Value: 1},
				{Key: "chat_id", Value: 1},
				{Key: "request_id", Value: 1},
			},
			Options: options.Index().SetUnique(true).SetName("assistant_runs_owner_chat_request"),
		},
		{
			Keys: bson.D{
				{Key: "owner_id", Value: 1},
				{Key: "chat_id", Value: 1},
				{Key: "run_id", Value: 1},
			},
			Options: options.Index().SetUnique(true).SetName("assistant_runs_owner_chat_run"),
		},
	})
	return err
}

// CreateOrGet inserts a new run or returns the existing one for the same
// owner, chat, and request ID. A different fingerprint returns request_conflict
// and does not insert a second document. Report plaintext is encrypted before
// any write; nil crypto or an encryption error writes nothing.
func (r *assistantRunRepository) CreateOrGet(ctx context.Context, run models.RunRecord) (models.RunRecord, error) {
	if err := validateNewRun(run); err != nil {
		return models.RunRecord{}, err
	}
	stored, err := r.prepareStored(run)
	if err != nil {
		return models.RunRecord{}, err
	}
	_, err = r.runs.InsertOne(ctx, stored)
	if err == nil {
		return stored.Clone(), nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return models.RunRecord{}, err
	}
	existing, findErr := r.findByRequest(ctx, stored.OwnerID, stored.ChatID, stored.RequestID)
	if findErr != nil {
		return models.RunRecord{}, findErr
	}
	if existing.RequestFingerprint != stored.RequestFingerprint {
		return models.RunRecord{}, models.NewValidationError(
			models.ErrorCodeRequestConflict,
			"request_id",
			"request_id already exists with a different fingerprint",
		)
	}
	return existing.Clone(), nil
}

// GetOwned returns the run only when owner, chat, and run ID all match.
func (r *assistantRunRepository) GetOwned(ctx context.Context, ownerID, chatID, runID string) (models.RunRecord, error) {
	if ownerID == "" || chatID == "" || runID == "" {
		return models.RunRecord{}, ErrRunNotFound
	}
	var record models.RunRecord
	err := r.runs.FindOne(ctx, bson.M{
		"owner_id": ownerID,
		"chat_id":  chatID,
		"run_id":   runID,
	}).Decode(&record)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.RunRecord{}, ErrRunNotFound
	}
	if err != nil {
		return models.RunRecord{}, err
	}
	return record.Clone(), nil
}

// Transition applies one legal conditional update. The filter includes owner,
// chat, run, expected status, and revision, so a concurrent writer or a
// terminal row makes this update fail. Encryption runs before the write.
func (r *assistantRunRepository) Transition(ctx context.Context, transition models.RunTransition) (models.RunRecord, error) {
	if err := validateTransition(transition); err != nil {
		return models.RunRecord{}, err
	}
	set, err := r.transitionSet(transition)
	if err != nil {
		return models.RunRecord{}, err
	}
	filter := bson.M{
		"owner_id": transition.OwnerID,
		"chat_id":  transition.ChatID,
		"run_id":   transition.RunID,
		"status":   transition.From,
		"revision": transition.ExpectedRevision,
	}
	var updated models.RunRecord
	err = r.runs.FindOneAndUpdate(ctx, filter, bson.M{
		"$set": set,
		"$inc": bson.M{"revision": int64(1)},
	}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.RunRecord{}, ErrTransitionConflict
	}
	if err != nil {
		return models.RunRecord{}, err
	}
	return updated.Clone(), nil
}

// InterruptOrphans marks nonterminal runs of every other instance as
// interrupted. Rows already terminal, and rows owned by ownerInstanceID, stay
// unchanged. This method does not read a source and does not start SQL.
func (r *assistantRunRepository) InterruptOrphans(ctx context.Context, ownerInstanceID string) (int64, error) {
	if ownerInstanceID == "" {
		return 0, errors.New("assistant interrupt requires the current instance id")
	}
	now := r.now().UTC()
	result, err := r.runs.UpdateMany(ctx, bson.M{
		"status":             bson.M{"$in": nonterminalStatuses()},
		"executing_instance": bson.M{"$ne": ownerInstanceID},
	}, bson.M{
		"$set": bson.M{
			"status":     models.RunStatusInterrupted,
			"stage":      models.RunStageInterrupted,
			"updated_at": now,
		},
		"$inc": bson.M{"revision": int64(1)},
	})
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, nil
	}
	return result.ModifiedCount, nil
}

// DecryptReport decrypts a stored payload. A cipher or decrypt failure returns
// an error and never the ciphertext bytes interpreted as a report.
func DecryptReport(cipher ReportCipher, record models.RunRecord) (*models.AnalysisReport, error) {
	if len(record.EncryptedResult) == 0 {
		return nil, nil
	}
	if cipher == nil {
		return nil, errors.New("assistant report cipher is nil")
	}
	plain, err := cipher.DecryptReport(record.EncryptedResult)
	if err != nil {
		return nil, err
	}
	var report models.AnalysisReport
	if err := json.Unmarshal(plain, &report); err != nil {
		return nil, fmt.Errorf("decode assistant report: %w", err)
	}
	return &report, nil
}

func (r *assistantRunRepository) prepareStored(run models.RunRecord) (models.RunRecord, error) {
	stored := run.Clone()
	if stored.SchemaVersion == 0 {
		stored.SchemaVersion = models.AssistantSchemaVersion
	}
	if stored.Revision == 0 {
		stored.Revision = 1
	}
	now := r.now().UTC()
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = now
	}
	if stored.UpdatedAt.IsZero() {
		stored.UpdatedAt = now
	}
	if len(run.EncryptedResult) > 0 {
		return models.RunRecord{}, errors.New("assistant run must not carry caller-supplied ciphertext")
	}
	stored.ReportPlaintext = nil
	if len(run.ReportPlaintext) > 0 {
		if r.cipher == nil {
			return models.RunRecord{}, errors.New("assistant report cipher is nil")
		}
		ciphertext, err := r.cipher.EncryptReport(run.ReportPlaintext)
		if err != nil {
			return models.RunRecord{}, err
		}
		stored.EncryptedResult = ciphertext
	}
	return stored, nil
}

func (r *assistantRunRepository) transitionSet(transition models.RunTransition) (bson.M, error) {
	updatedAt := transition.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = r.now()
	}
	set := bson.M{
		"status":     transition.To,
		"stage":      transition.Stage,
		"updated_at": updatedAt.UTC(),
	}
	if transition.ExecutingInstance != "" {
		set["executing_instance"] = transition.ExecutingInstance
	}
	if transition.CancellationMarked != nil {
		set["cancellation_marked"] = *transition.CancellationMarked
	}
	if transition.MessageID != "" {
		set["message_id"] = transition.MessageID
	}
	if transition.Version != nil {
		set["version"] = *transition.Version
	}
	if transition.SafeError != nil {
		copied := *transition.SafeError
		set["safe_error"] = copied
	}
	if len(transition.ReportPlaintext) > 0 {
		if r.cipher == nil {
			return nil, errors.New("assistant report cipher is nil")
		}
		ciphertext, err := r.cipher.EncryptReport(transition.ReportPlaintext)
		if err != nil {
			return nil, err
		}
		set["encrypted_result"] = ciphertext
	}
	return set, nil
}

func (r *assistantRunRepository) findByRequest(ctx context.Context, ownerID, chatID, requestID string) (models.RunRecord, error) {
	var record models.RunRecord
	err := r.runs.FindOne(ctx, bson.M{
		"owner_id":   ownerID,
		"chat_id":    chatID,
		"request_id": requestID,
	}).Decode(&record)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.RunRecord{}, ErrRunNotFound
	}
	if err != nil {
		return models.RunRecord{}, err
	}
	return record, nil
}

func validateNewRun(run models.RunRecord) error {
	if run.RunID == "" || run.OwnerID == "" || run.ChatID == "" || run.RequestID == "" {
		return models.NewValidationError(models.ErrorCodeMissingField, "run", "run_id, owner_id, chat_id, and request_id are required")
	}
	if run.RequestFingerprint == "" {
		return models.NewValidationError(models.ErrorCodeMissingField, "request_fingerprint", "request fingerprint is required")
	}
	switch run.Status {
	case models.RunStatusCreated, models.RunStatusRunning, models.RunStatusFailed:
	default:
		return models.NewValidationError(models.ErrorCodeInvalidEnum, "status", "a new run must be created, running, or failed")
	}
	return nil
}

func validateTransition(transition models.RunTransition) error {
	if transition.OwnerID == "" || transition.ChatID == "" || transition.RunID == "" {
		return models.NewValidationError(models.ErrorCodeMissingField, "run", "owner_id, chat_id, and run_id are required")
	}
	if !transition.From.Valid() || !transition.To.Valid() {
		return models.NewValidationError(models.ErrorCodeInvalidEnum, "status", "transition status is not a run status")
	}
	if transition.ExpectedRevision < 1 {
		return models.NewValidationError(models.ErrorCodeInvalidEnum, "revision", "expected revision must be positive")
	}
	if transition.Stage == "" {
		return models.NewValidationError(models.ErrorCodeMissingField, "stage", "transition stage is required")
	}
	if !transition.Legal() {
		return ErrTransitionConflict
	}
	if transition.To == models.RunStatusCompleted && len(transition.ReportPlaintext) == 0 {
		return models.NewValidationError(models.ErrorCodeMissingField, "report", "completed transition requires a report payload")
	}
	if transition.To != models.RunStatusCompleted && len(transition.ReportPlaintext) > 0 {
		return models.NewValidationError(models.ErrorCodeInvalidEnum, "report", "only a completed transition stores a report payload")
	}
	return nil
}

func nonterminalStatuses() bson.A {
	return bson.A{models.RunStatusCreated, models.RunStatusRunning}
}
