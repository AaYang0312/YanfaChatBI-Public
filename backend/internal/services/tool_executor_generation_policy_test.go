package services

import (
	"context"
	"yanfachatbi/pkg/llm"
	"testing"
)

// These exercise the real constructors; on Windows their package is Linux
// compile-only because dbmanager/sourcegate have platform-specific code.
func TestGenerateOnlyActualExecutorConstructorsFailClosed(t *testing.T) {
	for name, build := range map[string]func(GenerationToolOptions) llm.ToolExecutorFunc{
		"chat": func(o GenerationToolOptions) llm.ToolExecutorFunc {
			return BuildToolExecutor(nil, "owner", "chat", "postgresql", nil, o)
		},
		"dashboard": func(o GenerationToolOptions) llm.ToolExecutorFunc {
			return BuildDashboardToolExecutor(nil, "chat", "postgresql", nil, o)
		},
	} {
		t.Run(name, func(t *testing.T) {
			executor := build(GenerationToolOptions{})
			for _, tool := range []string{"execute_read_query", "execute_dashboard_query", "get_table_info", "get_knowledge_base", "analyze_product", "compare_sales", "fake_tool"} {
				r, err := executor(context.Background(), llm.ToolCall{Name: tool, Arguments: map[string]interface{}{"query": "SELECT 1", "table_names": []string{"secret"}}})
				if err != nil || r == nil || !r.IsError {
					t.Fatalf("%s was not denied before nil dependency: %+v %v", tool, r, err)
				}
			}
			calls := 0
			executor = build(GenerationToolOptions{UseCurrentProjection: true, GetTableInfo: func(context.Context, llm.ToolCall) (*llm.ToolResult, error) {
				calls++
				return &llm.ToolResult{Content: "current safe schema"}, nil
			}})
			r, err := executor(context.Background(), llm.ToolCall{Name: "get_table_info"})
			if err != nil || r == nil || r.Content != "current safe schema" || calls != 1 {
				t.Fatal("current metadata callback was not used")
			}
		})
	}
}
