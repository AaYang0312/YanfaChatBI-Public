package services

import (
	"context"
	"fmt"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
)

// DashboardProjection is metadata-only context plus callbacks that revalidate
// current scope on every tool invocation. Context includes only current safe KB.
type DashboardProjection struct {
	Context          string
	GetTableInfo     llm.ToolExecutorFunc
	GetKnowledgeBase llm.ToolExecutorFunc
}
type DashboardProjectionLoader func(context.Context, *models.Chat) (*DashboardProjection, error)
type DashboardGenerationOptions struct{ CurrentProjection DashboardProjectionLoader }
type dashboardGeneration struct {
	Context  string
	Tools    []llm.ToolDefinition
	Executor llm.ToolExecutorFunc
}

func prepareDashboardGeneration(ctx context.Context, chat *models.Chat, policy GenerationPolicy, useCurrentProjection bool, loader DashboardProjectionLoader, tools []llm.ToolDefinition, legacy llm.ToolExecutorFunc) (dashboardGeneration, error) {
	options := GenerationToolOptions{Policy: policy, UseCurrentProjection: useCurrentProjection}
	result := dashboardGeneration{}
	if useCurrentProjection || (!policy.AllowsAutomaticDataExecution() && loader != nil) {
		if loader == nil {
			return result, fmt.Errorf("current dashboard projection is not configured")
		}
		projection, err := loader(ctx, chat)
		if err != nil {
			return result, err
		}
		if projection == nil || projection.GetTableInfo == nil || projection.GetKnowledgeBase == nil {
			return result, fmt.Errorf("current dashboard projection is incomplete")
		}
		result.Context = projection.Context
		options.GetTableInfo = projection.GetTableInfo
		options.GetKnowledgeBase = projection.GetKnowledgeBase
	}
	result.Tools = policy.FilterTools(tools)
	result.Executor = WrapGenerationExecutor(legacy, options)
	return result, nil
}

// QueryObjectContextProvider is the current metadata-only service boundary.
// nil args requests all current safe structure/KB; non-nil args resolve a tool
// request within that same current projection. The implementation owns owner,
// source lease, feature flag, selection and effective-KB validation.
type QueryObjectContextProvider interface {
	QueryObjectStructure(ctx context.Context, userID, chatID string, args map[string]interface{}) (string, error)
}

// NewDashboardProjectionLoader adapts the chat service without retaining a
// snapshot or trusting any model-supplied owner/chat IDs. DI can pass this loader
// via DashboardGenerationOptions when constructing the dashboard service.
func NewDashboardProjectionLoader(provider QueryObjectContextProvider) DashboardProjectionLoader {
	return func(ctx context.Context, chat *models.Chat) (*DashboardProjection, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if provider == nil || chat == nil {
			return nil, fmt.Errorf("current query object context provider is unavailable")
		}
		ownerID, chatID := chat.UserID.Hex(), chat.ID.Hex()
		content, err := provider.QueryObjectStructure(ctx, ownerID, chatID, nil)
		if err != nil {
			return nil, err
		}
		read := func(ctx context.Context, call llm.ToolCall, args map[string]interface{}) (*llm.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			current, err := provider.QueryObjectStructure(ctx, ownerID, chatID, args)
			if err != nil {
				return nil, err
			}
			return &llm.ToolResult{CallID: call.ID, Name: call.Name, Content: llm.TruncateToolResult(current)}, nil
		}
		return &DashboardProjection{
			Context: content,
			GetTableInfo: func(ctx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
				args := call.Arguments
				// nil means ALL only for the provider's explicit context/KB
				// API. An empty get_table_info still needs server validation.
				if args == nil {
					args = map[string]interface{}{}
				}
				return read(ctx, call, args)
			},
			GetKnowledgeBase: func(ctx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
				return read(ctx, call, nil)
			},
		}, nil
	}
}
