package services

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"sync/atomic"
	"testing"
	"time"
)

// These call real service entry points with replaced business/model terminals.
// Full package runtime needs Linux/sourcegate; cross compilation is not execution.
func TestGenerateOnlyActualAutoRunKeepsSQLWithoutExecution(t *testing.T) {
	originalQuery, originalResponse := autoExecuteQuery, processLLMResponseForAutoRun
	t.Cleanup(func() { autoExecuteQuery = originalQuery; processLLMResponseForAutoRun = originalResponse })
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "strict", true: "allowed"}[allow], func(t *testing.T) {
			owner, id := primitive.NewObjectID(), primitive.NewObjectID()
			chat := &models.Chat{UserID: owner, Base: models.Base{ID: id}, Settings: models.ChatSettings{AutoExecuteQuery: allow}}
			stream := &assistantLifecycleStreamWait{done: make(chan struct{}, 4)}
			service := &chatService{chatRepo: &assistantLifecycleChatRepo{chat: chat}, sourceAccess: NewUnconfiguredSourceAccessService(nil), activeProcesses: map[string]context.CancelFunc{}, streamHandler: stream}
			calls := atomic.Int32{}
			autoExecuteQuery = func(*chatService, context.Context, string, string, *dtos.ExecuteQueryRequest) (*dtos.QueryExecutionResponse, uint32, error) {
				calls.Add(1)
				return &dtos.QueryExecutionResponse{}, 200, nil
			}
			processLLMResponseForAutoRun = func(_ *chatService, ctx context.Context, _, _, _, _ string) (*dtos.MessageResponse, error) {
				// A later setting change cannot authorize a request admitted as strict.
				chat.Settings.AutoExecuteQuery = true
				return &dtos.MessageResponse{ID: primitive.NewObjectID().Hex(), Queries: &[]dtos.Query{{ID: primitive.NewObjectID().Hex(), Query: `SELECT net FROM "reporting"."sales_view"`}}}, nil
			}
			if err := service.processLLMResponseAndRunQuery(context.Background(), owner.Hex(), id.Hex(), primitive.NewObjectID().Hex(), "mode-test"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-stream.done:
			case <-time.After(3 * time.Second):
				t.Fatal("auto-run did not finish")
			}
			want := int32(0)
			if allow {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("business calls=%d want %d", calls.Load(), want)
			}
		})
	}
}
func TestGenerateOnlyActualDataEntrypointsDenyBeforeDependencies(t *testing.T) {
	ctx := freezeGenerationContext(context.Background(), &models.Chat{})
	service := &chatService{}
	if _, _, err := service.ExecuteQuery(ctx, "owner", "chat", nil); !errors.Is(err, ErrGenerationOnly) {
		t.Fatal(err)
	}
	if _, err := service.ExecuteChartQuery(ctx, "owner", "chat", nil, 1); !errors.Is(err, ErrGenerationOnly) {
		t.Fatal(err)
	}
	if _, err := service.reExecuteQueryForVisualization(ctx, "chat", "SELECT COUNT(*) FROM reporting.v", "postgresql"); !errors.Is(err, ErrGenerationOnly) {
		t.Fatal(err)
	}
	if _, err := service.GenerateVisualizationForMessage(ctx, "owner", "chat", "message", "query", ""); !errors.Is(err, ErrGenerationOnly) {
		t.Fatal(err)
	}
}
