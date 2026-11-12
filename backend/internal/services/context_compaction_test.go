package services

import (
	"strings"
	"testing"
	"time"

	"yanfachatbi/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestUncoveredMessagesSkipsFoldedHistory(t *testing.T) {
	coveredID := primitive.NewObjectID()
	older := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	cutoff := older.Add(time.Hour)
	summary := &models.ChatContextSummary{
		CoveredMessageIDs: []primitive.ObjectID{coveredID},
		CoveredUntil:      &cutoff,
	}
	messages := []*models.Message{
		{Base: models.Base{ID: coveredID, CreatedAt: older}, Content: "covered by id"},
		{Base: models.Base{ID: primitive.NewObjectID(), CreatedAt: older}, Content: "covered by time"},
		{Base: models.Base{ID: primitive.NewObjectID(), CreatedAt: cutoff.Add(time.Minute)}, Content: "uncovered"},
	}
	out := uncoveredMessages(messages, summary)
	if len(out) != 1 || out[0].Content != "uncovered" {
		t.Fatalf("uncovered = %+v", out)
	}
	if out := uncoveredMessages(messages, nil); len(out) != 3 {
		t.Fatalf("no summary must return all: %d", len(out))
	}
}

func TestInjectContextSummaryPlacesSegmentAfterSystem(t *testing.T) {
	chat := &models.Chat{Base: models.Base{ID: primitive.NewObjectID()}}
	messages := []*models.LLMMessage{
		{Role: "system", Content: map[string]interface{}{"schema_update": "schema"}},
		{Role: "user", Content: map[string]interface{}{"user_message": "hi"}},
	}
	summary := &models.ChatContextSummary{Revision: 2, Rendered: "## 目标\n统计销量"}
	out := injectContextSummary(messages, summary, chat)
	if len(out) != 3 {
		t.Fatalf("len = %d", len(out))
	}
	if out[1].Content["context_summary"] != "## 目标\n统计销量" {
		t.Fatalf("summary not inserted at index 1: %+v", out[1].Content)
	}
	note, _ := out[1].Content["summary_note"].(string)
	if !strings.Contains(note, "schema") {
		t.Fatalf("note must defer to current schema: %q", note)
	}

	stale := &models.ChatContextSummary{Revision: 3, Rendered: "x", StaleSource: true}
	out = injectContextSummary(messages, stale, chat)
	note, _ = out[1].Content["summary_note"].(string)
	if !strings.Contains(note, "数据源已变更") {
		t.Fatalf("stale note = %q", note)
	}

	if got := injectContextSummary(messages, nil, chat); len(got) != 2 {
		t.Fatalf("nil summary changed messages: %d", len(got))
	}
	if got := injectContextSummary(messages, &models.ChatContextSummary{}, chat); len(got) != 2 {
		t.Fatalf("empty summary changed messages: %d", len(got))
	}
}

func TestEstimateMessagesTokens(t *testing.T) {
	messages := []*models.Message{
		{Content: "abcd"},
		nil,
		{Content: "你好"},
	}
	if got := estimateMessagesTokens(messages); got != 3 {
		t.Fatalf("estimate = %d", got)
	}
}
