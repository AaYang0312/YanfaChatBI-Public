package services

import (
	"context"
	"encoding/json"
	"yanfachatbi/pkg/llm"
)

// GenerationPolicy is a value frozen from the server's chat settings before a
// generation starts. Its zero value is strict generate-only. It never authorizes
// the separate, explicit user execution path or replaces source/owner checks.
type GenerationPolicy struct{ autoExecute bool }

func NewGenerationPolicy(autoExecute bool) GenerationPolicy {
	return GenerationPolicy{autoExecute: autoExecute}
}

// AllowsAutomaticDataExecution gates automatic final SQL, sampling, previews,
// visualization, count/explain and retries, in addition to the tool executor.
func (p GenerationPolicy) AllowsAutomaticDataExecution() bool { return p.autoExecute }

func (p GenerationPolicy) FilterTools(tools []llm.ToolDefinition) []llm.ToolDefinition {
	filtered := make([]llm.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if p.autoExecute || tool.Name == llm.GetTableInfoToolName || tool.Name == llm.FinalResponseToolName {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func generationToolError(call llm.ToolCall, message string) *llm.ToolResult {
	return &llm.ToolResult{CallID: call.ID, Name: call.Name, Content: message, IsError: true}
}

// WrapExecutor uses an exact allowlist, not the model's arguments or the offered
// tool list. The allowed metadata handler must itself be metadata-only. Final
// response calls serialize SQL for display without ever delegating to a runner.
func (p GenerationPolicy) WrapExecutor(next llm.ToolExecutorFunc) llm.ToolExecutorFunc {
	return func(ctx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !p.autoExecute {
			switch call.Name {
			case llm.FinalResponseToolName:
				content, err := json.Marshal(call.Arguments)
				if err != nil {
					return generationToolError(call, "Invalid final response"), nil
				}
				return &llm.ToolResult{CallID: call.ID, Name: call.Name, Content: string(content)}, nil
			case llm.GetTableInfoToolName:
				// Only the trusted metadata callback may run.
			default:
				return generationToolError(call, "GENERATION_ONLY: automatic data execution is disabled"), nil
			}
		}
		if next == nil {
			return generationToolError(call, "Tool executor is unavailable"), nil
		}
		return next(ctx, call)
	}
}

// GenerationToolOptions is supplied by the server, never decoded from model
// arguments. UseCurrentProjection prohibits falling back to legacy schema/KB.
// Callbacks must resolve the current authorized projection on every invocation.
type GenerationToolOptions struct {
	Policy               GenerationPolicy
	UseCurrentProjection bool
	GetTableInfo         llm.ToolExecutorFunc
	GetKnowledgeBase     llm.ToolExecutorFunc
}

// WrapGenerationExecutor installs metadata routing INSIDE the policy guard so
// callbacks cannot bypass strict mode. Legacy metadata is also disabled in strict
// mode unless a metadata-only callback is provided (old drivers may sample rows).
func WrapGenerationExecutor(next llm.ToolExecutorFunc, options GenerationToolOptions) llm.ToolExecutorFunc {
	return options.Policy.WrapExecutor(func(ctx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
		var metadata llm.ToolExecutorFunc
		switch call.Name {
		case llm.GetTableInfoToolName:
			metadata = options.GetTableInfo
		case "get_knowledge_base":
			metadata = options.GetKnowledgeBase
		default:
			if next == nil {
				return generationToolError(call, "Tool executor is unavailable"), nil
			}
			return next(ctx, call)
		}
		if metadata != nil {
			return metadata(ctx, call)
		}
		if options.UseCurrentProjection || !options.Policy.AllowsAutomaticDataExecution() {
			return generationToolError(call, "Current metadata-only projection is unavailable; refresh the schema"), nil
		}
		if next == nil {
			return generationToolError(call, "Tool executor is unavailable"), nil
		}
		return next(ctx, call)
	})
}

// A single optional server configuration preserves existing call sites. Reject
// ambiguous multiple options rather than allow later options to relax a guard.
func applyGenerationOptions(next llm.ToolExecutorFunc, options []GenerationToolOptions) llm.ToolExecutorFunc {
	if len(options) == 0 {
		return next
	}
	if len(options) != 1 {
		return func(_ context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
			return generationToolError(call, "Ambiguous generation policy configuration"), nil
		}
	}
	return WrapGenerationExecutor(next, options[0])
}
