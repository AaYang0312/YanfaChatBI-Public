package services

import (
	"context"
	"errors"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
	"testing"
)

type sourceBoundaryModel struct {
	llm.Client
	generate func(context.Context, llm.ToolExecutorFunc, llm.ToolCallConfig) (*llm.ToolCallResult, error)
}

func (m sourceBoundaryModel) GenerateWithTools(ctx context.Context, _ []*models.LLMMessage, _ []llm.ToolDefinition, e llm.ToolExecutorFunc, c llm.ToolCallConfig) (*llm.ToolCallResult, error) {
	return m.generate(ctx, e, c)
}
func TestQueryObjectSourceBoundarySurroundsModel(t *testing.T) {
	for _, boundary := range []string{"before", "iteration", "strict-tool", "final", "plain-result"} {
		t.Run(boundary, func(t *testing.T) {
			revoked := boundary == "before"
			checks, modelCalls, executorCalls := 0, 0, 0
			require := func(context.Context) error {
				checks++
				if revoked {
					return ErrSourceRecovering
				}
				return nil
			}
			model := sourceBoundaryModel{generate: func(ctx context.Context, e llm.ToolExecutorFunc, c llm.ToolCallConfig) (*llm.ToolCallResult, error) {
				modelCalls++
				revoked = true
				switch boundary {
				case "iteration":
					c.OnIteration(1, 0)
				case "strict-tool":
					_, _ = e(ctx, llm.ToolCall{Name: llm.ExecuteQueryToolName})
				case "final":
					c.OnToolCall(llm.ToolCall{Name: llm.FinalResponseToolName})
				}
				return &llm.ToolCallResult{Response: "must not persist"}, nil
			}}
			executor := NewGenerationPolicy(false).WrapExecutor(func(context.Context, llm.ToolCall) (*llm.ToolResult, error) { executorCalls++; return nil, nil })
			result, err := generateSourceWithTools(context.Background(), model, nil, nil, executor, llm.ToolCallConfig{}, require)
			if result != nil || !errors.Is(err, ErrSourceRecovering) || executorCalls != 0 || checks == 0 {
				t.Fatalf("result=%+v err=%v execution=%d checks=%d", result, err, executorCalls, checks)
			}
			if boundary == "before" && modelCalls != 0 {
				t.Fatal("revoked source reached model")
			}
		})
	}
}
