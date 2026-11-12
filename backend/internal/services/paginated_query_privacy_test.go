package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/llm"
	redisrepo "yanfachatbi/pkg/redis"
)

type paginationPrivacyModel struct {
	llm.Client
	generate func(context.Context, []*models.LLMMessage, []llm.ToolDefinition, llm.ToolExecutorFunc, llm.ToolCallConfig) (*llm.ToolCallResult, error)
}

func (c paginationPrivacyModel) GenerateWithTools(ctx context.Context, messages []*models.LLMMessage, tools []llm.ToolDefinition, executor llm.ToolExecutorFunc, cfg llm.ToolCallConfig) (*llm.ToolCallResult, error) {
	return c.generate(ctx, messages, tools, executor, cfg)
}

// Only the I/O driver is synthetic: the real Manager owns admission, execution,
// error propagation and transactions. No database or model endpoint is opened.
type paginationPrivacyDriver struct {
	dbmanager.DatabaseDriver
	execute func(string) (*dbmanager.QueryExecutionResult, error)
}

func (d paginationPrivacyDriver) Connect(dbmanager.ConnectionConfig) (*dbmanager.Connection, error) {
	return &dbmanager.Connection{}, nil
}
func (d paginationPrivacyDriver) Disconnect(*dbmanager.Connection) error { return nil }
func (d paginationPrivacyDriver) BeginTx(context.Context, *dbmanager.Connection) dbmanager.Transaction {
	return paginationPrivacyTransaction{execute: d.execute}
}

type paginationPrivacyTransaction struct {
	execute func(string) (*dbmanager.QueryExecutionResult, error)
}

func (t paginationPrivacyTransaction) Commit() error   { return nil }
func (t paginationPrivacyTransaction) Rollback() error { return nil }
func (t paginationPrivacyTransaction) ExecuteQuery(_ context.Context, query string) (*dbmanager.QueryExecutionResult, error) {
	return t.execute(query)
}

type paginationPrivacyCache struct{ redisrepo.IRedisRepositories }

func (paginationPrivacyCache) Get(string, context.Context) (string, error) {
	return "", errors.New("key does not exist")
}

func paginationPrivacyManager(t *testing.T, execute func(string) (*dbmanager.QueryExecutionResult, error)) *dbmanager.Manager {
	t.Helper()
	manager, err := dbmanager.NewManager(paginationPrivacyCache{}, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	manager.RegisterDriver("pagination-fixture", paginationPrivacyDriver{execute: execute})
	if err := manager.Connect(context.Background(), "pagination-chat", "pagination-owner", "", dbmanager.ConnectionConfig{
		Type: "pagination-fixture", Database: "synthetic-pagination", InitialSchemaDeferred: true,
	}); err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestPaginationPrivacyKeepsCursorAndDatabaseErrorsLocal(t *testing.T) {
	const cursor = "private-cursor-customer-9a031"
	const privateError = "private-db-detail-6b281"
	const privateRow = "private-result-row-4a191"
	const template = "SELECT '{{cursor_value}}' AS fixture_cursor"
	for _, initialError := range []string{
		"syntax error near " + cursor + " " + privateError,
		"invalid input syntax for integer: " + cursor + " " + privateError,
		"unclassified database failure " + cursor + " " + privateError,
	} {
		t.Run(strings.Fields(initialError)[0], func(t *testing.T) {
			var queries []string
			manager := paginationPrivacyManager(t, func(query string) (*dbmanager.QueryExecutionResult, error) {
				queries = append(queries, query)
				if len(queries) == 1 {
					return &dbmanager.QueryExecutionResult{Error: &dtos.QueryError{
						Code: "EXECUTION_ERROR", Message: "invalid input syntax: " + cursor + " " + privateError,
					}}, nil
				}
				return &dbmanager.QueryExecutionResult{Result: []map[string]interface{}{{"fixture_cursor": cursor, "value": privateRow}}}, nil
			})
			assertNoPrivateValue := func(label string, value interface{}) {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				for _, marker := range []string{cursor, privateError, privateRow} {
					if strings.Contains(string(raw), marker) {
						t.Errorf("%s disclosed a private canary", label)
					}
				}
			}
			client := paginationPrivacyModel{generate: func(ctx context.Context, messages []*models.LLMMessage, tools []llm.ToolDefinition, executor llm.ToolExecutorFunc, cfg llm.ToolCallConfig) (*llm.ToolCallResult, error) {
				assertNoPrivateValue("model messages", messages)
				assertNoPrivateValue("model tools", tools)
				assertNoPrivateValue("model system prompt", cfg.SystemPrompt)
				first, err := executor(ctx, llm.ToolCall{ID: "first", Name: "test_paginated_query", Arguments: map[string]interface{}{"query": template}})
				if err != nil || first == nil || !first.IsError {
					t.Fatalf("expected local failed query: %v", err)
				}
				assertNoPrivateValue("failed tool result", first)
				second, err := executor(ctx, llm.ToolCall{ID: "second", Name: "test_paginated_query", Arguments: map[string]interface{}{"query": template}})
				if err != nil || second == nil || second.IsError {
					t.Fatalf("expected local successful query: %v", err)
				}
				assertNoPrivateValue("successful tool result", second)
				return &llm.ToolCallResult{Response: `{"fixed_query":"SELECT '{{cursor_value}}' AS fixture_cursor"}`, Iterations: 2, TotalCalls: 2}, nil
			}}
			fixed, err := autoFixPaginatedQuery(context.Background(), client, manager, "pagination-chat", "postgresql", "SELECT", template, cursor, initialError)
			if err != nil || fixed != template {
				t.Fatalf("template repair changed behavior: %q %v", fixed, err)
			}
			want := "SELECT 'private-cursor-customer-9a031' AS fixture_cursor"
			if len(queries) != 2 || queries[0] != want || queries[1] != want {
				t.Fatalf("real cursor was not substituted only at the local executor: %#v", queries)
			}
		})
	}
}

func TestPaginationPrivacyRejectsConcreteToolQuery(t *testing.T) {
	executions := 0
	manager := paginationPrivacyManager(t, func(string) (*dbmanager.QueryExecutionResult, error) {
		executions++
		return &dbmanager.QueryExecutionResult{}, nil
	})
	client := paginationPrivacyModel{generate: func(ctx context.Context, _ []*models.LLMMessage, _ []llm.ToolDefinition, executor llm.ToolExecutorFunc, _ llm.ToolCallConfig) (*llm.ToolCallResult, error) {
		result, err := executor(ctx, llm.ToolCall{Name: "test_paginated_query", Arguments: map[string]interface{}{"query": "SELECT 1"}})
		if err != nil || result == nil || !result.IsError {
			t.Error("tool accepted a concrete query without the cursor placeholder")
		}
		return &llm.ToolCallResult{Response: `{"fixed_query":"SELECT '{{cursor_value}}'"}`}, nil
	}}
	if _, err := autoFixPaginatedQuery(context.Background(), client, manager, "pagination-chat", "postgresql", "SELECT", "SELECT '{{cursor_value}}'", "synthetic-cursor", "syntax error"); err != nil {
		t.Fatal(err)
	}
	if executions != 0 {
		t.Fatalf("concrete query reached local execution %d times", executions)
	}
}

func TestPaginationPrivacyPreservesSourceDenial(t *testing.T) {
	manager := paginationPrivacyManager(t, func(string) (*dbmanager.QueryExecutionResult, error) {
		return &dbmanager.QueryExecutionResult{Error: &dtos.QueryError{Code: "SOURCE_ACCESS_DENIED", Message: ErrSourceSyncing.Error()}}, nil
	})
	client := paginationPrivacyModel{generate: func(ctx context.Context, _ []*models.LLMMessage, _ []llm.ToolDefinition, executor llm.ToolExecutorFunc, _ llm.ToolCallConfig) (*llm.ToolCallResult, error) {
		_, err := executor(ctx, llm.ToolCall{Name: "test_paginated_query", Arguments: map[string]interface{}{"query": "SELECT '{{cursor_value}}'"}})
		if !errors.Is(err, ErrSourceSyncing) {
			t.Fatalf("source denial changed: %v", err)
		}
		return &llm.ToolCallResult{Response: `{"fixed_query":"SELECT '{{cursor_value}}'"}`}, nil
	}}
	_, err := autoFixPaginatedQuery(context.Background(), client, manager, "pagination-chat", "postgresql", "SELECT", "SELECT '{{cursor_value}}'", "synthetic-cursor", "syntax error")
	if !errors.Is(err, ErrSourceSyncing) {
		t.Fatalf("source denial was swallowed by repair: %v", err)
	}
}
