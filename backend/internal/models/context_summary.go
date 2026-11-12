package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ContextSummary is the structured, project-shaped conversation summary from
// spec §3.4. It stores digests and decisions only: never full SQL, result rows
// or credentials.
type ContextSummary struct {
	Goal            string                     `bson:"goal" json:"goal"`
	DataSource      ContextSummaryDataSource   `bson:"data_source" json:"data_source"`
	SchemaFacts     []ContextSummarySchemaFact `bson:"schema_facts" json:"schema_facts"`
	Queries         []ContextSummaryQuery      `bson:"queries" json:"queries"`
	Errors          []ContextSummaryError      `bson:"errors" json:"errors"`
	UserPreferences ContextSummaryPreferences  `bson:"user_preferences" json:"user_preferences"`
	Decisions       []string                   `bson:"decisions" json:"decisions"`
	OpenQuestions   []string                   `bson:"open_questions" json:"open_questions"`
	Provenance      ContextSummaryProvenance   `bson:"provenance" json:"provenance"`
}

// ContextSummaryDataSource pins the source and binding the summary was built
// against so a rebind can mark the summary stale (spec §3.4, §8).
type ContextSummaryDataSource struct {
	ChatID          primitive.ObjectID       `bson:"chat_id" json:"chat_id"`
	SourceID        string                   `bson:"source_id" json:"source_id"`
	BindingRevision int64                    `bson:"binding_revision" json:"binding_revision"`
	Database        string                   `bson:"database" json:"database"`
	Selection       *ContextSummarySelection `bson:"selection" json:"selection"`
}

// ContextSummarySelection records the object selection scope the summary saw.
type ContextSummarySelection struct {
	Kind     string   `bson:"kind" json:"kind"`
	Revision int64    `bson:"revision" json:"revision"`
	Objects  []string `bson:"objects" json:"objects"`
}

// ContextSummarySchemaFact is a structural fact confirmed in earlier turns. It
// is never schema authority; callers must defer to the object projection or
// get_table_info.
type ContextSummarySchemaFact struct {
	Object     string   `bson:"object" json:"object"`
	Kind       string   `bson:"kind" json:"kind"`
	KeyColumns []string `bson:"key_columns" json:"key_columns"`
	Notes      string   `bson:"notes" json:"notes"`
}

// ContextSummaryQuery stores a query digest, not the SQL body or result rows.
type ContextSummaryQuery struct {
	Purpose    string   `bson:"purpose" json:"purpose"`
	Tables     []string `bson:"tables" json:"tables"`
	SQLDigest  string   `bson:"sql_digest" json:"sql_digest"`
	RowCount   int64    `bson:"row_count" json:"row_count"`
	KeyNumbers []string `bson:"key_numbers" json:"key_numbers"`
	Status     string   `bson:"status" json:"status"`
}

// ContextSummaryError records an error and how it was fixed.
type ContextSummaryError struct {
	What     string `bson:"what" json:"what"`
	Cause    string `bson:"cause" json:"cause"`
	Fix      string `bson:"fix" json:"fix"`
	Resolved bool   `bson:"resolved" json:"resolved"`
}

// ContextSummaryPreferences carries the user preferences in force.
type ContextSummaryPreferences struct {
	ExecutionMode  string `bson:"execution_mode" json:"execution_mode"`
	NonTechMode    bool   `bson:"non_tech_mode" json:"non_tech_mode"`
	Model          string `bson:"model" json:"model"`
	ReasoningLevel string `bson:"reasoning_level" json:"reasoning_level"`
	Language       string `bson:"language" json:"language"`
}

// ContextSummaryProvenance records which messages the summary covers. Range
// keeps the spec's ISO-8601 string form so summary producers can supply the
// timestamps exactly as recorded in the message stream.
type ContextSummaryProvenance struct {
	MessageIDs   []primitive.ObjectID `bson:"message_ids" json:"message_ids"`
	Range        []string             `bson:"range" json:"range"`
	CoveredCount int64                `bson:"covered_count" json:"covered_count"`
	Model        string               `bson:"model" json:"model"`
	CreatedAt    time.Time            `bson:"created_at" json:"created_at"`
}

// ChatContextSummary is one document of the `chat_context_summaries`
// collection (spec §4). ChatID is unique; Revision advances by exactly one on
// every successful compaction.
type ChatContextSummary struct {
	Base              `bson:",inline"`
	ChatID            primitive.ObjectID   `bson:"chat_id" json:"chat_id"`
	Revision          int64                `bson:"revision" json:"revision"`
	Summary           ContextSummary       `bson:"summary" json:"summary"`
	Rendered          string               `bson:"rendered" json:"rendered"`
	TokenEstimate     int                  `bson:"token_estimate" json:"token_estimate"`
	CoveredMessageIDs []primitive.ObjectID `bson:"covered_message_ids" json:"covered_message_ids"`
	CoveredUntil      *time.Time           `bson:"covered_until" json:"covered_until"`
	Model             string               `bson:"model" json:"model"`
	StaleSource       bool                 `bson:"stale_source" json:"stale_source"`
}

// FilterForChat returns a copy of the summary that is safe to inject for the
// given chat. When the chat has been rebound to another source (or the binding
// revision changed), structural facts, query digests, errors and decisions from
// the old source are dropped and the summary is marked stale; the goal, user
// preferences and open questions survive. The caller must re-render the text.
func (s *ChatContextSummary) FilterForChat(chat *Chat) *ChatContextSummary {
	if s == nil {
		return nil
	}
	if chat == nil {
		return nil
	}
	out := *s
	out.CoveredMessageIDs = append([]primitive.ObjectID(nil), s.CoveredMessageIDs...)
	out.Summary = s.Summary
	out.Summary.SchemaFacts = append([]ContextSummarySchemaFact(nil), s.Summary.SchemaFacts...)
	out.Summary.Queries = append([]ContextSummaryQuery(nil), s.Summary.Queries...)
	out.Summary.Errors = append([]ContextSummaryError(nil), s.Summary.Errors...)
	out.Summary.Decisions = append([]string(nil), s.Summary.Decisions...)
	out.Summary.OpenQuestions = append([]string(nil), s.Summary.OpenQuestions...)

	stale := s.Summary.DataSource.SourceID != chat.SourceID ||
		s.Summary.DataSource.BindingRevision != chat.SourceBindingRevision
	if !stale {
		return &out
	}
	out.StaleSource = true
	out.Rendered = ""
	out.Summary.SchemaFacts = nil
	out.Summary.Queries = nil
	out.Summary.Errors = nil
	out.Summary.Decisions = nil
	return &out
}
