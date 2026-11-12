package services

import (
	"context"
	"errors"
	"yanfachatbi/internal/models"
)

var ErrGenerationOnly = errors.New("GENERATION_ONLY: automatic business data execution is disabled")

type generationContextKey struct{}

func freezeGenerationContext(ctx context.Context, chat *models.Chat) context.Context {
	if _, exists := ctx.Value(generationContextKey{}).(GenerationPolicy); exists {
		return ctx
	}
	return context.WithValue(ctx, generationContextKey{}, NewGenerationPolicy(chat != nil && chat.Settings.AutoExecuteQuery))
}
func frozenGenerationPolicy(ctx context.Context, chat *models.Chat) GenerationPolicy {
	if policy, ok := ctx.Value(generationContextKey{}).(GenerationPolicy); ok {
		return policy
	}
	return NewGenerationPolicy(chat != nil && chat.Settings.AutoExecuteQuery)
}
func isStrictGenerationContext(ctx context.Context) bool {
	policy, ok := ctx.Value(generationContextKey{}).(GenerationPolicy)
	return ok && !policy.AllowsAutomaticDataExecution()
}
func generationDataAllowed(ctx context.Context, chat *models.Chat) bool {
	return chat != nil && chat.Settings.AutoExecuteQuery && frozenGenerationPolicy(ctx, chat).AllowsAutomaticDataExecution()
}
func runAutomaticGenerationData(ctx context.Context, chat *models.Chat, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !generationDataAllowed(ctx, chat) {
		return ErrGenerationOnly
	}
	return run()
}
