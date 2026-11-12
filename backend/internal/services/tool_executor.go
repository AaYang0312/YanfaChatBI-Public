package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/llm"
)

const assistantToolWaitTimeout = 2 * time.Minute

// Source checks surround the SDK boundary as well as the executor. A strict
// tool refusal must not hide a source revocation, and final tools may bypass
// the executor in provider clients.
func generateSourceWithTools(ctx context.Context, client llm.Client, messages []*models.LLMMessage, tools []llm.ToolDefinition, executor llm.ToolExecutorFunc, config llm.ToolCallConfig, sourceChecks ...func(context.Context) error) (*llm.ToolCallResult, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	check := func() error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		for _, require := range sourceChecks {
			if require != nil {
				if err := require(ctx); err != nil {
					cancel(err)
					return err
				}
			}
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	iteration, toolCall := config.OnIteration, config.OnToolCall
	config.OnIteration = func(n, count int) {
		if check() == nil && iteration != nil {
			iteration(n, count)
		}
	}
	config.OnToolCall = func(call llm.ToolCall) {
		if check() == nil && toolCall != nil {
			toolCall(call)
		}
	}
	guarded := func(callCtx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
		if err := check(); err != nil {
			return nil, err
		}
		result, err := executor(callCtx, call)
		if _, denied := SourceAccessHTTPStatus(err); denied {
			cancel(err)
		}
		return result, err
	}
	result, err := client.GenerateWithTools(ctx, messages, tools, guarded, config)
	if sourceErr := check(); sourceErr != nil {
		return nil, sourceErr
	}
	return result, err
}

// assistantToolPollInterval is how often a business tool reloads a run that
// CreateRun started asynchronously. The tool waits for that same execution;
// it does not compute metrics itself.
const assistantToolPollInterval = 15 * time.Millisecond

// BuildToolExecutor creates a ToolExecutorFunc closure. userID and chatID are
// the server-known owner and Chat. assistant may be nil when this chat has no
// assistant service; business tools then fail closed. Model arguments are
// never read for owner or Chat. Automatic generation callers must pass a
// GenerationToolOptions value frozen from server settings; omission preserves
// the legacy interface only and does not imply a strict generation policy.
func BuildToolExecutor(
	dbMgr *dbmanager.Manager,
	userID string,
	chatID string,
	dbType string,
	assistant AssistantService,
	options ...GenerationToolOptions,
) llm.ToolExecutorFunc {
	executor := func(ctx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
		switch call.Name {
		case llm.ExecuteQueryToolName:
			return executeReadQuery(ctx, dbMgr, chatID, dbType, call)
		case llm.GetTableInfoToolName:
			return getTableInfo(ctx, dbMgr, chatID, dbType, call)
		case llm.AnalyzeProductToolName:
			return executeAssistantBusinessTool(ctx, assistant, userID, chatID, models.WorkflowProductAnalysis, call)
		case llm.CompareSalesToolName:
			return executeAssistantBusinessTool(ctx, assistant, userID, chatID, models.WorkflowSalesComparison, call)
		default:
			return &llm.ToolResult{
				CallID:  call.ID,
				Name:    call.Name,
				Content: fmt.Sprintf("Unknown tool: %s", call.Name),
				IsError: true,
			}, nil
		}
	}
	return applyGenerationOptions(executor, options)
}

// executeReadQuery handles the execute_read_query tool call.
func executeReadQuery(
	ctx context.Context,
	dbMgr *dbmanager.Manager,
	chatID string,
	dbType string,
	call llm.ToolCall,
) (*llm.ToolResult, error) {
	query, _ := call.Arguments["query"].(string)
	explanation, _ := call.Arguments["explanation"].(string)

	if query == "" {
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: "Error: 'query' argument is required",
			IsError: true,
		}, nil
	}

	// Validate read-only using centralized per-DB classification
	if !constants.IsReadOnlyQuery(query, dbType) {
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: "Error: Only read-only queries (SELECT, SHOW, DESCRIBE, EXPLAIN, or MongoDB find/aggregate) are allowed. Write operations must be included in the final response for user approval.",
			IsError: true,
		}, nil
	}

	log.Printf("ToolExecutor -> execute_read_query -> chatID=%s explanation=%q query=%q", chatID, explanation, query)

	// Execute the query using dbManager.
	// Pass empty strings for messageID, queryID, and streamID since this is a tool-call
	// exploration query, not a user-initiated execution.
	result, queryErr := dbMgr.ExecuteQuery(ctx, chatID, "", "", "", query, "SELECT", false, false)
	if queryErr != nil {
		if queryErr.Code == "SOURCE_ACCESS_DENIED" {
			return nil, sourceAccessCauseFromCode(queryErr.Message)
		}
		errContent := fmt.Sprintf("Query execution error [%s]: %s\nDetails: %s", queryErr.Code, queryErr.Message, queryErr.Details)
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: llm.TruncateToolResult(errContent),
			IsError: true,
		}, nil
	}

	// Marshal the result to JSON
	var content string
	if result.Result != nil {
		resultJSON, err := json.Marshal(result.Result)
		if err != nil {
			content = fmt.Sprintf("Query executed successfully but failed to serialize result: %v", err)
		} else {
			content = string(resultJSON)
		}
	} else {
		content = "Query executed successfully. No rows returned."
	}

	// Include execution time metadata
	if result.ExecutionTime > 0 {
		content = fmt.Sprintf("Execution time: %dms\n%s", result.ExecutionTime, content)
	}

	return &llm.ToolResult{
		CallID:  call.ID,
		Name:    call.Name,
		Content: llm.TruncateToolResult(content),
		IsError: false,
	}, nil
}

// getTableInfo handles the get_table_info tool call.
func getTableInfo(
	ctx context.Context,
	dbMgr *dbmanager.Manager,
	chatID string,
	dbType string,
	call llm.ToolCall,
) (*llm.ToolResult, error) {
	// Parse table_names from arguments
	tableNamesRaw, ok := call.Arguments["table_names"]
	if !ok {
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: "Error: 'table_names' argument is required",
			IsError: true,
		}, nil
	}

	var tableNames []string
	switch v := tableNamesRaw.(type) {
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				tableNames = append(tableNames, s)
			}
		}
	case []string:
		tableNames = v
	default:
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: fmt.Sprintf("Error: 'table_names' must be an array of strings, got %T", tableNamesRaw),
			IsError: true,
		}, nil
	}

	if len(tableNames) == 0 {
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: "Error: 'table_names' must contain at least one table name",
			IsError: true,
		}, nil
	}

	log.Printf("ToolExecutor -> get_table_info -> chatID=%s tables=%v", chatID, tableNames)

	// Get the database connection
	dbConn, err := dbMgr.GetConnectionContext(ctx, chatID)
	if err != nil {
		if _, denied := SourceAccessHTTPStatus(err); denied {
			return nil, err
		}
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: fmt.Sprintf("Error getting database connection: %v", err),
			IsError: true,
		}, nil
	}

	// Fetch schema for the specific tables using SchemaManager
	schema, err := dbMgr.GetSchemaManager().GetSchema(ctx, chatID, dbConn, dbType, tableNames)
	if err != nil {
		if _, denied := SourceAccessHTTPStatus(err); denied {
			return nil, err
		}
		return &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: fmt.Sprintf("Error fetching schema for tables %v: %v", tableNames, err),
			IsError: true,
		}, nil
	}

	// Format the schema for LLM readability
	content := dbMgr.GetSchemaManager().FormatSchemaForLLM(schema)

	// Detect table name mismatches (e.g. spreadsheet 'sheet_data' → actual 'ventures').
	// When the schema driver maps raw table names to user-facing names, the returned
	// schema may contain different table names than what the LLM requested.
	// Prepend a clear note so the LLM uses the correct names in its SQL.
	requested := make(map[string]bool, len(tableNames))
	for _, t := range tableNames {
		requested[t] = true
	}
	var mismatches []string
	for actualName := range schema.Tables {
		if !requested[actualName] {
			mismatches = append(mismatches, actualName)
		}
	}
	if len(mismatches) > 0 {
		var note string
		for _, actual := range mismatches {
			note += fmt.Sprintf("⚠️ IMPORTANT: The table you requested was mapped to '%s' in the database. "+
				"You MUST use '%s' as the table name in all your SQL queries, NOT the name from information_schema.\n", actual, actual)
		}
		content = note + content
		log.Printf("ToolExecutor -> get_table_info -> Table name mismatch detected: requested=%v, actual schema tables=%v", tableNames, mismatches)
	}

	return &llm.ToolResult{
		CallID:  call.ID,
		Name:    call.Name,
		Content: llm.TruncateToolResult(content),
		IsError: false,
	}, nil
}

// assistantToolForbiddenArguments are identity and data-access fields. A model
// argument with any of these names is rejected before the assistant service
// is called, so it cannot override the closure's owner or Chat and cannot
// smuggle SQL, schema names, or credentials.
var assistantToolForbiddenArguments = []string{
	"owner", "owner_id", "userid", "user_id", "user",
	"chat", "chat_id", "chatid",
	"sql", "query", "schema", "schema_name", "credentials", "password",
}

// assistantToolContent is the only payload a business tool returns to the model.
// Report JSON, queries, and chart coordinates are not fields of this type.
type assistantToolContent struct {
	Status     string   `json:"status"`
	RunID      string   `json:"run_id,omitempty"`
	MessageID  string   `json:"message_id,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Missing    []string `json:"missing_fields,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	Code       string   `json:"code,omitempty"`
	Field      string   `json:"field,omitempty"`
	Message    string   `json:"message,omitempty"`
}

// executeAssistantBusinessTool runs one business tool against the shared
// AssistantService. Owner and Chat come from the executor closure. Resolve
// runs first; CreateRun runs only after the draft resolves. Execution is the
// same asynchronous path HTTP uses, and the summary is read back from the
// persisted, decrypted report — the tool never recomputes metrics.
func executeAssistantBusinessTool(
	ctx context.Context,
	assistant AssistantService,
	userID string,
	chatID string,
	workflow models.Workflow,
	call llm.ToolCall,
) (*llm.ToolResult, error) {
	if assistant == nil {
		return assistantToolError(call, "assistant service is not configured"), nil
	}
	if forbidden := assistantForbiddenArgument(call.Arguments); forbidden != "" {
		return assistantToolError(call, "argument "+forbidden+" is not accepted; owner and chat are server-known"), nil
	}
	draft, err := assistantDraftFromArguments(call.Arguments, workflow)
	if err != nil {
		return assistantToolServiceError(call, err), nil
	}

	access := models.AccessContext{OwnerID: userID, ChatID: chatID}
	resolution, err := assistant.Resolve(ctx, access, draft)
	if err != nil {
		if _, denied := SourceAccessHTTPStatus(err); denied {
			return nil, err
		}
		return assistantToolServiceError(call, err), nil
	}
	if resolution.Status == models.ResolveStatusNeedsInput {
		return assistantToolResult(call, false, assistantToolContent{
			Status:     string(models.ResolveStatusNeedsInput),
			Missing:    resolution.MissingFields,
			Candidates: assistantCandidateLabels(resolution.Candidates),
			Summary:    assistantNeedsInputSummary(resolution),
		}), nil
	}
	if resolution.Status != models.ResolveStatusResolved || resolution.Request == nil {
		return assistantToolError(call, "assistant resolve did not return an executable request"), nil
	}
	created, err := assistant.CreateRun(ctx, access, *resolution.Request)
	if err != nil {
		if _, denied := SourceAccessHTTPStatus(err); denied {
			return nil, err
		}
		return assistantToolServiceError(call, err), nil
	}
	view, err := assistantWaitForRun(ctx, assistant, access, created)
	if err != nil {
		return assistantToolServiceError(call, err), nil
	}
	return assistantToolResult(call, view.Status == models.RunStatusFailed, assistantToolContent{
		Status:    string(view.Status),
		RunID:     view.RunID,
		MessageID: view.MessageID,
		Summary:   assistantRunSummary(view, view.Report),
	}), nil
}

func assistantForbiddenArgument(arguments map[string]interface{}) string {
	for key := range arguments {
		normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
		for _, forbidden := range assistantToolForbiddenArguments {
			if normalized == forbidden {
				return key
			}
		}
	}
	return ""
}

func assistantDraftFromArguments(arguments map[string]interface{}, workflow models.Workflow) (models.DraftRequest, error) {
	body, err := json.Marshal(arguments)
	if err != nil {
		return models.DraftRequest{}, models.NewValidationError(models.ErrorCodeInvalidSchema, "arguments",
			"business tool arguments are not valid JSON")
	}
	if err := assistantRejectNumericThreshold(body); err != nil {
		return models.DraftRequest{}, err
	}
	var draft models.DraftRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return models.DraftRequest{}, models.NewValidationError(models.ErrorCodeInvalidSchema, "arguments",
			err.Error())
	}
	if draft.SchemaVersion == 0 {
		draft.SchemaVersion = models.AssistantSchemaVersion
	}
	if draft.Workflow == "" {
		draft.Workflow = workflow
	}
	if draft.Workflow != workflow {
		return models.DraftRequest{}, models.NewValidationError(models.ErrorCodeInvalidEnum, "workflow",
			"workflow "+string(draft.Workflow)+" does not match tool "+string(workflow))
	}
	if len(draft.Metrics) == 0 {
		draft.Metrics = models.SupportedMetrics()
	}
	return draft, nil
}

// assistantWaitForRun waits for the run CreateRun already started. A replay
// that is already terminal is returned as stored. The tool never starts a
// second execution.
func assistantWaitForRun(ctx context.Context, assistant AssistantService, access models.AccessContext, created models.RunView) (models.RunView, error) {
	waitCtx, cancel := context.WithTimeout(ctx, assistantToolWaitTimeout)
	defer cancel()
	return assistantFinishedView(waitCtx, assistant, access, created)
}

func assistantFinishedView(ctx context.Context, assistant AssistantService, access models.AccessContext, created models.RunView) (models.RunView, error) {
	deadline := time.Now().Add(assistantToolWaitTimeout)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	for {
		view, err := assistant.GetRun(ctx, access, created.RunID)
		if err != nil {
			return models.RunView{}, err
		}
		if view.Status.IsTerminal() {
			return view, nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return view, nil
		}
		timer := time.NewTimer(assistantToolPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return view, nil
		case <-timer.C:
		}
	}
}

func assistantCandidateLabels(candidates []models.Candidate) []string {
	labels := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		label := candidate.Label
		if label == "" {
			label = candidate.EntityID
		}
		labels = append(labels, string(candidate.EntityType)+":"+label)
	}
	return labels
}

func assistantNeedsInputSummary(resolution models.Resolution) string {
	parts := []string{"needs_input"}
	if len(resolution.MissingFields) > 0 {
		parts = append(parts, "missing "+strings.Join(resolution.MissingFields, ","))
	}
	if len(resolution.Candidates) > 0 {
		parts = append(parts, "candidates "+strings.Join(assistantCandidateLabels(resolution.Candidates), ","))
	}
	return assistantBoundSummary(strings.Join(parts, "; "))
}

func assistantRunSummary(view models.RunView, report *models.AnalysisReport) string {
	if report == nil {
		if view.SafeError != nil {
			return assistantBoundSummary(view.SafeError.Code + ": " + view.SafeError.Message)
		}
		return assistantBoundSummary("status " + string(view.Status))
	}
	parts := []string{
		report.MarginLabel,
		"workflow " + string(report.Workflow),
		"basis " + string(report.Basis),
		"period " + report.Window.StartDate + ".." + report.Window.EndDateExclusive,
		"outcome " + string(report.Outcome),
	}
	for _, total := range report.Totals {
		value := "null"
		if total.Value != nil {
			value = total.Value.String()
		}
		parts = append(parts, string(total.MetricID)+"="+value)
	}
	if report.Refunds.Amount != nil {
		parts = append(parts, "refunds="+report.Refunds.Amount.String())
	}
	return assistantBoundSummary(strings.Join(parts, "; "))
}

func assistantBoundSummary(summary string) string {
	summary = strings.Join(strings.Fields(summary), " ")
	runes := []rune(summary)
	if len(runes) <= constants.AssistantToolSummaryLimit {
		return summary
	}
	return string(runes[:constants.AssistantToolSummaryLimit])
}

func assistantToolResult(call llm.ToolCall, isError bool, payload assistantToolContent) *llm.ToolResult {
	payload.Summary = assistantBoundSummary(payload.Summary)
	body, err := json.Marshal(payload)
	if err != nil {
		return assistantToolError(call, "failed to encode the assistant tool result")
	}
	return &llm.ToolResult{
		CallID:  call.ID,
		Name:    call.Name,
		Content: string(body),
		IsError: isError,
	}
}

func assistantToolError(call llm.ToolCall, message string) *llm.ToolResult {
	return assistantToolResult(call, true, assistantToolContent{
		Status:  "error",
		Summary: message,
		Message: message,
	})
}

func assistantToolServiceError(call llm.ToolCall, err error) *llm.ToolResult {
	if IsAssistantNotFound(err) {
		return assistantToolResult(call, true, assistantToolContent{
			Status:  "not_found",
			Code:    "404",
			Summary: "assistant resource not found",
			Message: "assistant resource not found",
		})
	}
	var validation *models.ValidationError
	if errors.As(err, &validation) {
		status := "error"
		if code, ok := validation.Code.HTTPStatus(); ok {
			status = fmt.Sprintf("%d", code)
		}
		return assistantToolResult(call, true, assistantToolContent{
			Status:  status,
			Code:    string(validation.Code),
			Field:   validation.Field,
			Summary: validation.Error(),
			Message: validation.Message,
		})
	}

	return assistantToolError(call, "assistant request failed")
}

// assistantRejectNumericThreshold rejects a JSON number. The decimal type
// accepts both, and the contract requires the ratio to stay a string.
func assistantRejectNumericThreshold(body []byte) error {
	var probe struct {
		LowMarginThreshold json.RawMessage `json:"low_margin_threshold"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || len(probe.LowMarginThreshold) == 0 {
		return nil
	}
	if probe.LowMarginThreshold[0] != '"' {
		return models.NewValidationError(models.ErrorCodeInvalidSchema, "low_margin_threshold",
			"low_margin_threshold is a decimal string, never a JSON number")
	}
	return nil
}
