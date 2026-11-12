package services

import (
	"context"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
	"testing"
)

func TestGenerateOnlyDashboardUsesMetadataProviderForLegacyTables(t *testing.T) {
	loads, reads, data := 0, 0, 0
	metadata := func(context.Context, llm.ToolCall) (*llm.ToolResult, error) {
		reads++
		return &llm.ToolResult{Content: "metadata"}, nil
	}
	loader := func(context.Context, *models.Chat) (*DashboardProjection, error) {
		loads++
		return &DashboardProjection{Context: "safe table metadata", GetTableInfo: metadata, GetKnowledgeBase: metadata}, nil
	}
	legacy := func(context.Context, llm.ToolCall) (*llm.ToolResult, error) { data++; return &llm.ToolResult{}, nil }
	p, err := prepareDashboardGeneration(context.Background(), &models.Chat{}, NewGenerationPolicy(false), false, loader, llm.GetChatTools(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Executor(context.Background(), llm.ToolCall{Name: llm.GetTableInfoToolName})
	if err != nil || r.IsError || loads != 1 || reads != 1 || data != 0 || p.Context != "safe table metadata" {
		t.Fatalf("provider routing missing: loads=%d reads=%d data=%d result=%+v context=%q", loads, reads, data, r, p.Context)
	}
}
