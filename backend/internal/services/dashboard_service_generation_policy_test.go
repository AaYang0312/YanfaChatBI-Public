package services

import (
	"context"
	"yanfachatbi/config"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestGenerateOnlyDashboardActualProviderBoundary(t *testing.T) {
	previous := config.Env.QueryObjectsEnabled
	config.Env.QueryObjectsEnabled = true
	defer func() { config.Env.QueryObjectsEnabled = previous }()
	chat := &models.Chat{Connection: models.Connection{Type: constants.DatabaseTypePostgreSQL}}
	metadata := func(context.Context, llm.ToolCall) (*llm.ToolResult, error) {
		return &llm.ToolResult{Content: "public.safe_view: amount numeric"}, nil
	}
	service := &dashboardService{generationOptions: DashboardGenerationOptions{CurrentProjection: func(context.Context, *models.Chat) (*DashboardProjection, error) {
		return &DashboardProjection{Context: "current safe schema and KB", GetTableInfo: metadata, GetKnowledgeBase: metadata}, nil
	}}}
	result, err := service.generateDashboardWithTools(context.Background(), chat, NewGenerationPolicy(false), generationMaliciousClient{t: t}, nil, GetDashboardTools(constants.DashboardModeGenerate), llm.ToolCallConfig{DBType: constants.DatabaseTypePostgreSQL})
	if err != nil || result == nil || result.Response == "" {
		t.Fatalf("real dashboard boundary failed: %+v %v", result, err)
	}
	config.Env.QueryObjectsEnabled = false
	chat.SelectedObjects = &schemaobjects.Selection{}
	if _, err = service.generateDashboardWithTools(context.Background(), chat, NewGenerationPolicy(true), nil, nil, GetDashboardTools(constants.DashboardModeBlueprint), llm.ToolCallConfig{}); err == nil {
		t.Fatal("disabled v2 reached provider or legacy path")
	}
	config.Env.QueryObjectsEnabled = true
	service.generationOptions.CurrentProjection = nil
	// nil client proves failure precedes any provider call.
	if _, err = service.generateDashboardWithTools(context.Background(), chat, NewGenerationPolicy(false), nil, nil, GetDashboardTools(constants.DashboardModeBlueprint), llm.ToolCallConfig{}); err == nil {
		t.Fatal("missing v2 projection reached provider")
	}
}
