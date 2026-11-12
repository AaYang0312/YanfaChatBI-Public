package services

import (
	"context"
	"yanfachatbi/internal/models"
	"testing"
)

func TestGenerateOnlyFrozenAcrossAutomaticDescendants(t *testing.T) {
	chat := &models.Chat{Settings: models.ChatSettings{AutoExecuteQuery: false}}
	ctx := freezeGenerationContext(context.Background(), chat)
	chat.Settings.AutoExecuteQuery = true
	ctx = freezeGenerationContext(ctx, chat)
	for _, path := range []string{"final SQL", "count", "explain", "sample", "visualization", "retry"} {
		t.Run(path, func(t *testing.T) {
			called := 0
			err := runAutomaticGenerationData(ctx, chat, func() error { called++; return nil })
			if err == nil || called != 0 {
				t.Fatal("strict request executed data")
			}
		})
	}
	if !isStrictGenerationContext(ctx) {
		t.Fatal("strict context lost")
	}
	if isStrictGenerationContext(context.Background()) {
		t.Fatal("manual execution lost separate authorization")
	}
	allowed := freezeGenerationContext(context.Background(), chat)
	called := 0
	if err := runAutomaticGenerationData(allowed, chat, func() error { called++; return nil }); err != nil || called != 1 {
		t.Fatal("allowed free SQL blocked")
	}
	chat.Settings.AutoExecuteQuery = false
	if err := runAutomaticGenerationData(allowed, chat, func() error { called++; return nil }); err == nil || called != 1 {
		t.Fatal("current disable ignored")
	}
}
