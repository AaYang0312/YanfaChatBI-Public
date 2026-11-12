package services

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/llm"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type visualizationPrivacyClient struct {
	llm.Client
	system, prompt, data string
}

func (c *visualizationPrivacyClient) GenerateVisualization(_ context.Context, system, prompt, data string, _ ...string) (string, error) {
	c.system, c.prompt, c.data = system, prompt, data
	return "{\"can_visualize\":true,\"chart_configuration\":{\"chart_type\":\"bar\"}}", nil
}

type visualizationPrivacyChatRepo struct {
	repositories.ChatRepository
	chat    *models.Chat
	message *models.Message
}

func (r visualizationPrivacyChatRepo) FindByID(primitive.ObjectID) (*models.Chat, error) {
	return r.chat, nil
}

func (r visualizationPrivacyChatRepo) FindMessageByID(primitive.ObjectID) (*models.Message, error) {
	return r.message, nil
}

func (r visualizationPrivacyChatRepo) FindLatestMessageByChat(primitive.ObjectID, int, int) ([]*models.Message, int64, error) {
	return []*models.Message{r.message}, 1, nil
}

func (visualizationPrivacyChatRepo) UpdateQueryVisualizationID(primitive.ObjectID, primitive.ObjectID, primitive.ObjectID) error {
	return nil
}

type visualizationPrivacyRepo struct {
	repositories.IVisualizationRepository
}

func (visualizationPrivacyRepo) GetVisualizationByQueryID(context.Context, interface{}) (*models.MessageVisualization, error) {
	return nil, nil
}

func (visualizationPrivacyRepo) CreateVisualization(context.Context, *models.MessageVisualization) error {
	return nil
}

func TestVisualizationModelRequestOmitsRawRows(t *testing.T) {
	const marker = "distinctive-private-customer-6d3fa"
	const query = "SELECT customer, amount FROM sales"
	rows := []map[string]interface{}{{"customer": marker, "amount": 871235.5}}
	saved, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	executionResult := string(saved)
	queryID := primitive.NewObjectID()
	owner := primitive.NewObjectID()

	for _, mode := range []string{"automatic", "manual_saved_result"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			chat := models.NewChat(owner, models.Connection{Type: "postgresql"}, models.ChatSettings{
				AutoGenerateVisualization: mode == "automatic", AutoExecuteQuery: mode == "automatic",
			})
			message := &models.Message{
				UserID: owner, ChatID: chat.ID, Type: "assistant", Base: models.NewBase(),
				Queries: &[]models.Query{{ID: queryID, Query: query, IsExecuted: true, ExecutionResult: &executionResult}},
			}
			client := &visualizationPrivacyClient{}
			service := &chatService{
				chatRepo:          visualizationPrivacyChatRepo{chat: chat, message: message},
				sourceAccess:      NewUnconfiguredSourceAccessService(nil),
				visualizationRepo: visualizationPrivacyRepo{},
				llmClient:         client,
			}

			var response *dtos.VisualizationResponse
			var err error
			if mode == "automatic" {
				response, err = service.GenerateVisualizationForQueryResults(ctx, owner.Hex(), chat.ID.Hex(), chat,
					"fixture-model", "Show sales by customer", []interface{}{map[string]interface{}{"query": query}}, rows, false)
			} else {
				response, err = service.GenerateVisualizationForMessage(ctx, owner.Hex(), chat.ID.Hex(), message.ID.Hex(), queryID.Hex(), "fixture-model")
			}
			if err != nil || response == nil || !response.CanVisualize {
				t.Fatalf("generate chart: response=%+v err=%v", response, err)
			}
			if !reflect.DeepEqual(response.ChartData, rows) {
				t.Fatalf("chart response lost its local rows: %#v", response.ChartData)
			}
			for _, part := range []string{client.system, client.prompt, client.data} {
				if strings.Contains(part, marker) || strings.Contains(part, "871235.5") {
					t.Fatal("raw fixture row value reached the model")
				}
			}
			var modelData struct {
				ColumnCount   int
				ColumnTypes   map[string]string
				TotalRowCount int
			}
			if err := json.Unmarshal([]byte(client.data), &modelData); err != nil {
				t.Fatalf("invalid serialized model data: %v", err)
			}
			wantTypes := map[string]string{"customer": "string", "amount": "numeric"}
			if modelData.ColumnCount != 2 || modelData.TotalRowCount != 1 || !reflect.DeepEqual(modelData.ColumnTypes, wantTypes) {
				t.Fatalf("missing model metadata: %+v", modelData)
			}
		})
	}
}
