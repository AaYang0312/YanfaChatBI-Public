package services

import (
	"context"
	"yanfachatbi/pkg/llm"
)

// generationMetadataTool is the real chat metadata callback. Internal context
// loaders may use nil for all current structure; a model tool call may not.
func generationMetadataTool(read func(context.Context, map[string]interface{}) (string, error)) llm.ToolExecutorFunc {
	return func(ctx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
		args := call.Arguments
		if args == nil {
			args = map[string]interface{}{}
		}
		text, err := read(ctx, args)
		if err != nil {
			return generationToolError(call, "Current object metadata unavailable"), err
		}
		return &llm.ToolResult{CallID: call.ID, Name: call.Name, Content: text}, nil
	}
}
