package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Context summary generation and second-level compaction (spec §3.3–§3.6).
//
// The generator is deliberately narrow: the service only needs raw JSON
// generation, so production passes *llm.OpenAIClient and tests pass a fake.
type rawJSONGenerator interface {
	GenerateRawJSON(ctx context.Context, systemPrompt, userMessage string, modelID ...string) (string, error)
}

const (
	// ContextSummaryTimeout bounds one summary call (spec §3.3).
	ContextSummaryTimeout = 15 * time.Second

	// ContextSummaryGoalMaxChars caps the free-form goal.
	ContextSummaryGoalMaxChars = 2000
	// ContextSummaryNotesMaxChars caps one schema fact note.
	ContextSummaryNotesMaxChars = 2000
	// ContextSummaryMaxSchemaFacts caps the schema fact list.
	ContextSummaryMaxSchemaFacts = 50
	// ContextSummaryMaxQueries caps the query digest list.
	ContextSummaryMaxQueries = 50
	// ContextSummaryMaxErrors caps the error list.
	ContextSummaryMaxErrors = 50

	// contextSummaryDefaultMaxTokens matches CONTEXT_SUMMARY_MAX_TOKENS.
	contextSummaryDefaultMaxTokens = 4000
	// contextSummaryRecentQueries is how many recent query digests survive
	// second-level compaction (spec §3.5).
	contextSummaryRecentQueries = 3
	// contextSummaryInputMaxTokens bounds the folded-message payload.
	contextSummaryInputMaxTokens = 24000
	// contextSummaryMessageMaxTokens bounds one folded message.
	contextSummaryMessageMaxTokens = 1200
	// contextSummaryPreviousMaxTokens bounds the previous summary JSON. A
	// stored summary is already compacted, this is only a safety valve.
	contextSummaryPreviousMaxTokens = 6000
)

// contextSummarySystemPrompt carries the hard rules from spec §3.4. It is a
// package constant so tests can assert the privacy and authority clauses.
const contextSummarySystemPrompt = `你是长对话压缩器。把“已有摘要”和“待折叠消息”合并成一份结构化摘要，只输出一个 JSON 对象，不要输出解释、Markdown 代码块或任何多余文本。

输出 JSON 必须使用以下字段；没有内容的字段用空数组或空串，禁止输出 null：
{"goal":"", "data_source":{"chat_id":"", "source_id":"", "binding_revision":0, "database":"", "selection":{"kind":"", "revision":0, "objects":[]}}, "schema_facts":[{"object":"", "kind":"", "key_columns":[], "notes":""}], "queries":[{"purpose":"", "tables":[], "sql_digest":"", "row_count":0, "key_numbers":[], "status":"executed|generated-only|failed"}], "errors":[{"what":"", "cause":"", "fix":"", "resolved":false}], "user_preferences":{"execution_mode":"strict|normal", "non_tech_mode":false, "model":"", "reasoning_level":"", "language":""}, "decisions":[], "open_questions":[], "provenance":{"message_ids":[], "range":[], "covered_count":0, "model":"", "created_at":""}}

硬性规则：
1. 只输出 JSON；字段名与类型必须与上述 schema 一致。
2. 禁止输出完整 SQL：queries 只能写 purpose、tables、sql_digest（短摘要或哈希）与 status，禁止粘贴 SQL 语句、DDL 或执行计划。
3. 禁止输出结果数据行：只能写 row_count 与 key_numbers 这类简短聚合，禁止复制表格行、单元格内容或原始结果集。
4. 禁止输出任何凭据：密码、连接串、token、API key、私钥、个人信息一律不得出现在摘要中。
5. schema_facts 只是历史对话中确认过的结构事实，不是当前数据库结构的权威；注入时会声明“可能过期，当前结构以对象目录 / get_table_info 为准”，不要把它描述成当前 schema。
6. 已有摘要必须合并保留，不得丢失 goal、open_questions、user_preferences；新信息覆盖旧细节，冲突时保留更新的结论。
7. 空字段使用空数组或空串。`

// GenerateContextSummary folds messages into the chat's structured summary.
//
// It returns a new record with revision = previous.Revision + 1 (1 when there
// is no previous summary), provenance covering exactly the supplied messages,
// and rendered Markdown trimmed to CONTEXT_SUMMARY_MAX_TOKENS when it would
// otherwise exceed the limit (spec §3.5). Generation, parse and limit failures
// return an error or a degraded record; this function never panics.
func GenerateContextSummary(ctx context.Context, gen rawJSONGenerator, chat *models.Chat, previous *models.ChatContextSummary, messages []*models.Message, model string) (*models.ChatContextSummary, error) {
	if gen == nil {
		return nil, errors.New("CONTEXT_SUMMARY_GENERATOR_MISSING")
	}
	if chat == nil {
		return nil, errors.New("CONTEXT_SUMMARY_CHAT_MISSING")
	}
	if len(messages) == 0 {
		return nil, errors.New("CONTEXT_SUMMARY_NO_MESSAGES")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := contextSummaryTimeoutContext(ctx)
	defer cancel()

	userMessage, err := buildContextSummaryUserMessage(previous, messages)
	if err != nil {
		return nil, err
	}

	var raw string
	if model == "" {
		raw, err = gen.GenerateRawJSON(ctx, contextSummarySystemPrompt, userMessage)
	} else {
		raw, err = gen.GenerateRawJSON(ctx, contextSummarySystemPrompt, userMessage, model)
	}
	if err != nil {
		return nil, fmt.Errorf("context summary generation failed: %w", err)
	}

	generated, err := parseContextSummaryJSON(raw)
	if err != nil {
		return nil, err
	}

	merged := mergeContextSummary(previous, generated)
	normalizeContextSummary(&merged)
	merged.DataSource = contextSummaryDataSource(chat)

	coveredIDs, rangeStamps, coveredUntil := contextSummaryCoverage(messages)
	merged.Provenance = models.ContextSummaryProvenance{
		MessageIDs:   coveredIDs,
		Range:        rangeStamps,
		CoveredCount: int64(len(coveredIDs)),
		Model:        model,
		CreatedAt:    time.Now().UTC(),
	}

	merged, rendered := CompactContextSummary(merged, contextSummaryTokenLimit())

	revision := int64(1)
	if previous != nil && previous.Revision > 0 {
		revision = previous.Revision + 1
	}

	return &models.ChatContextSummary{
		ChatID:            chat.ID,
		Revision:          revision,
		Summary:           merged,
		Rendered:          rendered,
		TokenEstimate:     llm.EstimateTokens(rendered),
		CoveredMessageIDs: coveredIDs,
		CoveredUntil:      coveredUntil,
		Model:             model,
		StaleSource:       false,
	}, nil
}

// CompactContextSummary applies spec §3.5 second-level compaction and returns
// the trimmed summary plus its rendered Markdown. The returned rendered text
// is guaranteed to stay within maxTokens when maxTokens > 0.
func CompactContextSummary(summary models.ContextSummary, maxTokens int) (models.ContextSummary, string) {
	rendered := RenderContextSummary(summary)
	if maxTokens <= 0 || llm.EstimateTokens(rendered) <= maxTokens {
		return summary, rendered
	}

	compacted := cloneContextSummary(summary)

	// Stage 1: keep goal / open_questions / user_preferences / latest queries,
	// keep only unresolved errors, reduce schema facts to object names, drop
	// decisions (spec §3.5).
	if len(compacted.Queries) > contextSummaryRecentQueries {
		compacted.Queries = compacted.Queries[len(compacted.Queries)-contextSummaryRecentQueries:]
	}
	compacted.Errors = contextSummaryUnresolvedErrors(compacted.Errors)
	for i := range compacted.SchemaFacts {
		compacted.SchemaFacts[i].KeyColumns = nil
		compacted.SchemaFacts[i].Notes = ""
	}
	compacted.Decisions = nil
	rendered = RenderContextSummary(compacted)
	if llm.EstimateTokens(rendered) <= maxTokens {
		return compacted, rendered
	}

	// Stage 2: shed the remaining optional detail while keeping the priority
	// fields, then shorten the free-form strings proportionally.
	compacted.SchemaFacts = nil
	compacted.Errors = nil
	for i := range compacted.Queries {
		compacted.Queries[i].Tables = nil
		compacted.Queries[i].KeyNumbers = nil
		compacted.Queries[i].SQLDigest = ""
	}
	compacted.Goal = truncateContextSummaryRunes(compacted.Goal, 500)
	for i := range compacted.OpenQuestions {
		compacted.OpenQuestions[i] = truncateContextSummaryRunes(compacted.OpenQuestions[i], 300)
	}
	rendered = RenderContextSummary(compacted)
	if llm.EstimateTokens(rendered) <= maxTokens {
		return compacted, rendered
	}

	// Stage 3: hard guarantee. Shorten the free-form priority fields, re-render
	// so the structured summary matches, then trim the text as a last resort.
	compacted.Goal = truncateContextSummaryRunes(compacted.Goal, maxTokens/4)
	for i := range compacted.OpenQuestions {
		compacted.OpenQuestions[i] = truncateContextSummaryRunes(compacted.OpenQuestions[i], max(80, maxTokens/8))
	}
	if candidate := RenderContextSummary(compacted); llm.EstimateTokens(candidate) <= maxTokens {
		return compacted, candidate
	}
	rendered, _ = llm.TrimToTokenBudget(rendered, maxTokens)
	return compacted, rendered
}

// RenderContextSummary renders the structured summary as the fixed Markdown
// injected into prompts (spec §3.4).
func RenderContextSummary(summary models.ContextSummary) string {
	var b strings.Builder

	b.WriteString("## 目标\n")
	b.WriteString(firstNonEmpty(summary.Goal, "（无）"))
	b.WriteString("\n\n")

	b.WriteString("## 数据源\n")
	ds := summary.DataSource
	writeContextSummaryBullet(&b, "source_id", ds.SourceID)
	writeContextSummaryBullet(&b, "database", ds.Database)
	writeContextSummaryBullet(&b, "binding_revision", fmt.Sprintf("%d", ds.BindingRevision))
	if ds.Selection != nil {
		writeContextSummaryBullet(&b, "selection", fmt.Sprintf("%s (revision %d)", firstNonEmpty(ds.Selection.Kind, "unknown"), ds.Selection.Revision))
		if len(ds.Selection.Objects) > 0 {
			writeContextSummaryBullet(&b, "objects", strings.Join(ds.Selection.Objects, ", "))
		}
	}
	if ds.SourceID == "" && ds.Database == "" && ds.Selection == nil {
		b.WriteString("- （无）\n")
	}
	b.WriteString("\n")

	b.WriteString("## 已确认结构\n")
	b.WriteString("> 以下只是历史对话中确认过的结构事实，可能过期；当前结构以对象目录 / get_table_info 为准。\n")
	if len(summary.SchemaFacts) == 0 {
		b.WriteString("- （无）\n")
	}
	for _, fact := range summary.SchemaFacts {
		fmt.Fprintf(&b, "- %s", fact.Object)
		if fact.Kind != "" {
			fmt.Fprintf(&b, " (%s)", fact.Kind)
		}
		if len(fact.KeyColumns) > 0 {
			fmt.Fprintf(&b, " | key_columns=%s", strings.Join(fact.KeyColumns, ", "))
		}
		if fact.Notes != "" {
			fmt.Fprintf(&b, " | notes=%s", fact.Notes)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 已执行查询\n")
	if len(summary.Queries) == 0 {
		b.WriteString("- （无）\n")
	}
	for _, query := range summary.Queries {
		fmt.Fprintf(&b, "- [%s] %s", firstNonEmpty(query.Status, "unknown"), firstNonEmpty(query.Purpose, "（未命名查询）"))
		if len(query.Tables) > 0 {
			fmt.Fprintf(&b, " | tables=%s", strings.Join(query.Tables, ", "))
		}
		if query.SQLDigest != "" {
			fmt.Fprintf(&b, " | sql_digest=%s", query.SQLDigest)
		}
		fmt.Fprintf(&b, " | rows=%d", query.RowCount)
		if len(query.KeyNumbers) > 0 {
			fmt.Fprintf(&b, " | key_numbers=%s", strings.Join(query.KeyNumbers, "; "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 错误与修复\n")
	if len(summary.Errors) == 0 {
		b.WriteString("- （无）\n")
	}
	for _, item := range summary.Errors {
		state := "未解决"
		if item.Resolved {
			state = "已解决"
		}
		fmt.Fprintf(&b, "- [%s] %s", state, firstNonEmpty(item.What, "（未知错误）"))
		if item.Cause != "" {
			fmt.Fprintf(&b, " | cause=%s", item.Cause)
		}
		if item.Fix != "" {
			fmt.Fprintf(&b, " | fix=%s", item.Fix)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 用户偏好\n")
	prefs := summary.UserPreferences
	prefParts := make([]string, 0, 5)
	if prefs.ExecutionMode != "" {
		prefParts = append(prefParts, "execution_mode="+prefs.ExecutionMode)
	}
	if prefs.NonTechMode {
		prefParts = append(prefParts, "non_tech_mode=true")
	}
	if prefs.Model != "" {
		prefParts = append(prefParts, "model="+prefs.Model)
	}
	if prefs.ReasoningLevel != "" {
		prefParts = append(prefParts, "reasoning_level="+prefs.ReasoningLevel)
	}
	if prefs.Language != "" {
		prefParts = append(prefParts, "language="+prefs.Language)
	}
	if len(prefParts) == 0 {
		b.WriteString("- （无）\n")
	} else {
		fmt.Fprintf(&b, "- %s\n", strings.Join(prefParts, "; "))
	}
	b.WriteString("\n")

	b.WriteString("## 决策\n")
	writeContextSummaryList(&b, summary.Decisions)
	b.WriteString("\n")

	b.WriteString("## 未决问题\n")
	writeContextSummaryList(&b, summary.OpenQuestions)

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// contextSummaryModelOutput mirrors the model-facing schema but treats the two
// server-authoritative blocks as opaque JSON: data_source and provenance come
// from the chat and the covered message range, never from the model.
type contextSummaryModelOutput struct {
	Goal            string                            `json:"goal"`
	DataSource      json.RawMessage                   `json:"data_source"`
	SchemaFacts     []models.ContextSummarySchemaFact `json:"schema_facts"`
	Queries         []models.ContextSummaryQuery      `json:"queries"`
	Errors          []models.ContextSummaryError      `json:"errors"`
	UserPreferences models.ContextSummaryPreferences  `json:"user_preferences"`
	Decisions       []string                          `json:"decisions"`
	OpenQuestions   []string                          `json:"open_questions"`
	Provenance      json.RawMessage                   `json:"provenance"`
}

// parseContextSummaryJSON accepts a bare JSON object or one wrapped in a
// Markdown code fence / prose. Schema errors are returned to the caller.
func parseContextSummaryJSON(raw string) (models.ContextSummary, error) {
	candidate := extractJSONObject(raw)
	if candidate == "" {
		return models.ContextSummary{}, fmt.Errorf("CONTEXT_SUMMARY_INVALID_JSON: no JSON object in response")
	}
	var output contextSummaryModelOutput
	if err := json.Unmarshal([]byte(candidate), &output); err != nil {
		return models.ContextSummary{}, fmt.Errorf("CONTEXT_SUMMARY_INVALID_JSON: %w", err)
	}
	return models.ContextSummary{
		Goal:            output.Goal,
		SchemaFacts:     output.SchemaFacts,
		Queries:         output.Queries,
		Errors:          output.Errors,
		UserPreferences: output.UserPreferences,
		Decisions:       output.Decisions,
		OpenQuestions:   output.OpenQuestions,
	}, nil
}

func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return ""
	}
	return raw[start : end+1]
}

// mergeContextSummary combines the previous summary with the freshly generated
// one. goal / open_questions / user_preferences and earlier schema facts are
// never dropped (spec §3.4).
func mergeContextSummary(previous *models.ChatContextSummary, generated models.ContextSummary) models.ContextSummary {
	if previous == nil {
		return generated
	}
	merged := cloneContextSummary(previous.Summary)

	if generated.Goal != "" {
		merged.Goal = generated.Goal
	}
	merged.Decisions = mergeContextSummaryStrings(merged.Decisions, generated.Decisions)
	merged.OpenQuestions = mergeContextSummaryStrings(merged.OpenQuestions, generated.OpenQuestions)
	merged.UserPreferences = mergeContextSummaryPreferences(merged.UserPreferences, generated.UserPreferences)
	merged.SchemaFacts = mergeContextSummarySchemaFacts(merged.SchemaFacts, generated.SchemaFacts)
	merged.Queries = mergeContextSummaryQueries(merged.Queries, generated.Queries)
	merged.Errors = mergeContextSummaryErrors(merged.Errors, generated.Errors)
	return merged
}

func mergeContextSummaryStrings(previous, generated []string) []string {
	out := append([]string(nil), previous...)
	seen := make(map[string]struct{}, len(out))
	for _, item := range out {
		seen[item] = struct{}{}
	}
	for _, item := range generated {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func mergeContextSummaryPreferences(previous, generated models.ContextSummaryPreferences) models.ContextSummaryPreferences {
	out := previous
	if generated.ExecutionMode != "" {
		out.ExecutionMode = generated.ExecutionMode
	}
	if generated.NonTechMode {
		out.NonTechMode = true
	}
	if generated.Model != "" {
		out.Model = generated.Model
	}
	if generated.ReasoningLevel != "" {
		out.ReasoningLevel = generated.ReasoningLevel
	}
	if generated.Language != "" {
		out.Language = generated.Language
	}
	return out
}

func mergeContextSummarySchemaFacts(previous, generated []models.ContextSummarySchemaFact) []models.ContextSummarySchemaFact {
	out := append([]models.ContextSummarySchemaFact(nil), previous...)
	index := make(map[string]int, len(out))
	for i, fact := range out {
		if fact.Object != "" {
			index[fact.Object] = i
		}
	}
	for _, fact := range generated {
		if fact.Object == "" {
			continue
		}
		if i, ok := index[fact.Object]; ok {
			out[i] = fact
			continue
		}
		index[fact.Object] = len(out)
		out = append(out, fact)
	}
	return out
}

func mergeContextSummaryQueries(previous, generated []models.ContextSummaryQuery) []models.ContextSummaryQuery {
	out := append([]models.ContextSummaryQuery(nil), previous...)
	for _, query := range generated {
		key := query.Purpose + "|" + query.SQLDigest
		for i, existing := range out {
			if existing.Purpose+"|"+existing.SQLDigest == key {
				// Update in place, then move to the end so "most recent"
				// ordering survives compaction.
				out = append(out[:i], out[i+1:]...)
				break
			}
		}
		out = append(out, query)
	}
	return out
}

func mergeContextSummaryErrors(previous, generated []models.ContextSummaryError) []models.ContextSummaryError {
	out := append([]models.ContextSummaryError(nil), previous...)
	index := make(map[string]int, len(out))
	for i, item := range out {
		if item.What != "" {
			index[item.What] = i
		}
	}
	for _, item := range generated {
		if item.What == "" {
			continue
		}
		if i, ok := index[item.What]; ok {
			out[i] = item
			continue
		}
		index[item.What] = len(out)
		out = append(out, item)
	}
	return out
}

// normalizeContextSummary enforces the size caps from the T03 contract.
func normalizeContextSummary(summary *models.ContextSummary) {
	if summary == nil {
		return
	}
	summary.Goal = truncateContextSummaryRunes(summary.Goal, ContextSummaryGoalMaxChars)
	if len(summary.SchemaFacts) > ContextSummaryMaxSchemaFacts {
		summary.SchemaFacts = summary.SchemaFacts[:ContextSummaryMaxSchemaFacts]
	}
	for i := range summary.SchemaFacts {
		summary.SchemaFacts[i].Notes = truncateContextSummaryRunes(summary.SchemaFacts[i].Notes, ContextSummaryNotesMaxChars)
	}
	if len(summary.Queries) > ContextSummaryMaxQueries {
		summary.Queries = summary.Queries[:ContextSummaryMaxQueries]
	}
	if len(summary.Errors) > ContextSummaryMaxErrors {
		summary.Errors = summary.Errors[:ContextSummaryMaxErrors]
	}
}

func contextSummaryUnresolvedErrors(items []models.ContextSummaryError) []models.ContextSummaryError {
	if len(items) == 0 {
		return nil
	}
	out := make([]models.ContextSummaryError, 0, len(items))
	for _, item := range items {
		if item.Resolved {
			continue
		}
		out = append(out, item)
	}
	return out
}

func contextSummaryTimeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, ContextSummaryTimeout)
}

func contextSummaryTokenLimit() int {
	limit := config.Env.ContextSummaryMaxTokens
	if limit <= 0 {
		return contextSummaryDefaultMaxTokens
	}
	return limit
}

func contextSummaryDataSource(chat *models.Chat) models.ContextSummaryDataSource {
	ds := models.ContextSummaryDataSource{
		ChatID:          chat.ID,
		SourceID:        chat.SourceID,
		BindingRevision: chat.SourceBindingRevision,
		Database:        chat.Connection.Database,
	}
	if chat.SelectedObjects == nil {
		ds.Selection = &models.ContextSummarySelection{Kind: "legacy", Revision: chat.SelectionRevision}
		return ds
	}
	objects := make([]string, 0, len(chat.SelectedObjects.Objects))
	for _, ref := range chat.SelectedObjects.Objects {
		name := ref.Name
		if ref.Schema != "" {
			name = ref.Schema + "." + ref.Name
		}
		objects = append(objects, name)
	}
	ds.Selection = &models.ContextSummarySelection{
		Kind:     firstNonEmpty(chat.SelectedObjects.Mode, "v2"),
		Revision: chat.SelectionRevision,
		Objects:  objects,
	}
	return ds
}

// contextSummaryCoverage records which messages the summary covers. Only
// messages with an ID are recorded so the repository never stores zero IDs.
func contextSummaryCoverage(messages []*models.Message) ([]primitive.ObjectID, []string, *time.Time) {
	ids := make([]primitive.ObjectID, 0, len(messages))
	var firstAt, lastAt time.Time
	for _, message := range messages {
		if message == nil {
			continue
		}
		if !message.ID.IsZero() {
			ids = append(ids, message.ID)
		}
		if message.CreatedAt.IsZero() {
			continue
		}
		if firstAt.IsZero() {
			firstAt = message.CreatedAt
		}
		lastAt = message.CreatedAt
	}
	var rangeStamps []string
	if !firstAt.IsZero() && !lastAt.IsZero() {
		rangeStamps = []string{firstAt.UTC().Format(time.RFC3339), lastAt.UTC().Format(time.RFC3339)}
	}
	var coveredUntil *time.Time
	if last := messages[len(messages)-1]; last != nil && !last.CreatedAt.IsZero() {
		stamp := last.CreatedAt
		coveredUntil = &stamp
	}
	return ids, rangeStamps, coveredUntil
}

func buildContextSummaryUserMessage(previous *models.ChatContextSummary, messages []*models.Message) (string, error) {
	var b strings.Builder
	b.WriteString("## 已有摘要（JSON，可能为空）\n")
	if previous == nil {
		b.WriteString("（无）\n")
	} else {
		encoded, err := json.Marshal(previous.Summary)
		if err != nil {
			return "", fmt.Errorf("context summary previous marshal failed: %w", err)
		}
		encodedText := string(encoded)
		if llm.EstimateTokens(encodedText) > contextSummaryPreviousMaxTokens {
			encodedText, _ = llm.TrimToTokenBudget(encodedText, contextSummaryPreviousMaxTokens)
		}
		b.WriteString(encodedText)
		b.WriteString("\n")
	}
	b.WriteString("\n## 待折叠消息（按时间升序）\n")
	b.WriteString(renderContextSummaryMessages(messages))
	b.WriteString("\n请合并以上内容，只输出 JSON。")
	return b.String(), nil
}

func renderContextSummaryMessages(messages []*models.Message) string {
	blocks := make([]string, 0, len(messages))
	for i, message := range messages {
		if message == nil {
			continue
		}
		stamp := "unknown"
		if !message.CreatedAt.IsZero() {
			stamp = message.CreatedAt.UTC().Format(time.RFC3339)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "[%d] role=%s at=%s\n", i+1, message.Type, stamp)
		b.WriteString(renderContextSummaryMessageBody(message))
		blocks = append(blocks, b.String())
	}

	total := 0
	for _, block := range blocks {
		total += llm.EstimateTokens(block) + 4
	}
	start := 0
	for start < len(blocks)-1 && total > contextSummaryInputMaxTokens {
		total -= llm.EstimateTokens(blocks[start]) + 4
		start++
	}
	return strings.Join(blocks[start:], "\n")
}

func renderContextSummaryMessageBody(message *models.Message) string {
	content := message.Content
	if message.Type == "assistant" {
		var parsed struct {
			Response string         `json:"response"`
			Query    []models.Query `json:"query"`
		}
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			var b strings.Builder
			if parsed.Response != "" {
				b.WriteString(parsed.Response)
			}
			for _, query := range parsed.Query {
				// Only the digest-level fields: never the SQL body or rows.
				b.WriteString("\n- query: ")
				b.WriteString(firstNonEmpty(query.Description, "（未命名查询）"))
				if query.QueryType != nil && *query.QueryType != "" {
					fmt.Fprintf(&b, " | type=%s", *query.QueryType)
				}
				if query.Tables != nil && *query.Tables != "" {
					fmt.Fprintf(&b, " | tables=%s", *query.Tables)
				}
				fmt.Fprintf(&b, " | executed=%t", query.IsExecuted)
				if query.Error != nil && query.Error.Message != "" {
					fmt.Fprintf(&b, " | error=%s", query.Error.Message)
				}
			}
			content = b.String()
		}
	}
	content, _ = llm.TrimToTokenBudget(content, contextSummaryMessageMaxTokens)
	if strings.TrimSpace(content) == "" {
		return "（空）\n"
	}
	return content + "\n"
}

func writeContextSummaryBullet(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", key, value)
}

func writeContextSummaryList(b *strings.Builder, items []string) {
	if len(items) == 0 {
		b.WriteString("- （无）\n")
		return
	}
	for _, item := range items {
		if item == "" {
			continue
		}
		fmt.Fprintf(b, "- %s\n", item)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// truncateContextSummaryRunes cuts s to at most max runes. A truncated string
// keeps a single-rendered ellipsis inside the budget.
func truncateContextSummaryRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

func cloneContextSummary(summary models.ContextSummary) models.ContextSummary {
	out := summary
	out.Decisions = append([]string(nil), summary.Decisions...)
	out.OpenQuestions = append([]string(nil), summary.OpenQuestions...)

	out.SchemaFacts = make([]models.ContextSummarySchemaFact, len(summary.SchemaFacts))
	for i, fact := range summary.SchemaFacts {
		out.SchemaFacts[i] = fact
		out.SchemaFacts[i].KeyColumns = append([]string(nil), fact.KeyColumns...)
	}

	out.Queries = make([]models.ContextSummaryQuery, len(summary.Queries))
	for i, query := range summary.Queries {
		out.Queries[i] = query
		out.Queries[i].Tables = append([]string(nil), query.Tables...)
		out.Queries[i].KeyNumbers = append([]string(nil), query.KeyNumbers...)
	}

	out.Errors = append([]models.ContextSummaryError(nil), summary.Errors...)

	if summary.DataSource.Selection != nil {
		selection := *summary.DataSource.Selection
		selection.Objects = append([]string(nil), summary.DataSource.Selection.Objects...)
		out.DataSource.Selection = &selection
	}

	out.Provenance.MessageIDs = append([]primitive.ObjectID(nil), summary.Provenance.MessageIDs...)
	out.Provenance.Range = append([]string(nil), summary.Provenance.Range...)
	return out
}
