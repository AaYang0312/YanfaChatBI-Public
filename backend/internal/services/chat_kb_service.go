package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/utils"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// GetKnowledgeBase retrieves the knowledge base for a chat.
func (s *chatService) GetKnowledgeBase(ctx context.Context, userID, chatID string) (*models.KnowledgeBase, uint32, error) {
	log.Printf("ChatService -> GetKnowledgeBase -> chatID: %s", chatID)

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID")
	}

	// Verify user owns the chat
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil || chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}

	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil || chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized")
	}

	if queryObjectsEnabled(chat) {
		view, status, err := s.getQueryObjectKnowledgeBase(ctx, userID, chatID)
		if err != nil {
			return nil, status, err
		}
		return &models.KnowledgeBase{ChatID: chat.ID, UserID: chat.UserID, KBRevision: view.KBRevision, TableDescriptions: view.TableDescriptions}, status, nil
	}
	if s.kbRepo == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("knowledge base not available")
	}

	kb, err := s.kbRepo.FindByChatID(ctx, chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch knowledge base: %v", err)
	}

	if kb != nil && (!kb.UserID.IsZero() && kb.UserID != chat.UserID) {
		return nil, http.StatusForbidden, ErrObjectKnowledgeScope
	}
	if kb != nil && kb.KnowledgeVersion >= 2 {
		return nil, http.StatusConflict, ErrQueryObjectsDisabled
	}
	// Return empty KB if none exists yet
	if kb == nil {
		kb = models.NewKnowledgeBase(chatObjID)
	}

	return kb, http.StatusOK, nil
}

// UpdateKnowledgeBase saves metadata. ERP embedding requires explicit admitted refresh.
func (s *chatService) UpdateKnowledgeBase(ctx context.Context, userID, chatID string, tableDescs []models.TableDescription) (*models.KnowledgeBase, uint32, error) {
	log.Printf("ChatService -> UpdateKnowledgeBase -> chatID: %s, tables: %d", chatID, len(tableDescs))

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID")
	}

	// Verify user owns the chat
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil || chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}

	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil || chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized")
	}

	if queryObjectsEnabled(chat) {
		return nil, http.StatusConflict, ErrObjectKnowledgeRevision
	}
	if s.kbRepo == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("knowledge base not available")
	}

	// Get or create KB
	kb, err := s.kbRepo.FindByChatID(ctx, chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch knowledge base: %v", err)
	}
	if kb != nil && (!kb.UserID.IsZero() && kb.UserID != chat.UserID) {
		return nil, http.StatusForbidden, ErrObjectKnowledgeScope
	}
	if kb != nil && kb.KnowledgeVersion >= 2 {
		return nil, http.StatusConflict, ErrQueryObjectsDisabled
	}
	if kb == nil {
		kb = models.NewKnowledgeBase(chatObjID)
	}

	// Set user ID and update table descriptions
	kb.UserID = userObjID
	kb.TableDescriptions = tableDescs

	// Save to MongoDB
	if err := s.kbRepo.Upsert(ctx, kb); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to save knowledge base: %v", err)
	}

	log.Printf("ChatService -> UpdateKnowledgeBase -> Saved KB with %d table descriptions", len(tableDescs))

	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	classification, _ := s.sourcePolicy().Classify(chat, connection)
	if classification != models.SourceIndependent {
		return kb, http.StatusOK, nil
	}
	if err := s.vectorizeSchemaForChat(ctx, chatID); err != nil {
		return nil, http.StatusInternalServerError, err
	}

	return kb, http.StatusOK, nil
}

// syncKnowledgeBase joins the actual model and persistence terminals.
func (s *chatService) syncKnowledgeBase(ctx context.Context, chatID string, formattedSchema string) error {
	if s.kbRepo == nil {
		return nil
	}

	log.Printf("ChatService -> syncKnowledgeBase -> Starting KB sync for chatID: %s", chatID)

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		log.Printf("ChatService -> syncKnowledgeBase -> Invalid chatID: %v", err)
		return err
	}

	// Get the chat to obtain userID
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil || chat == nil {
		log.Printf("ChatService -> syncKnowledgeBase -> Chat not found: %v", err)
		if err != nil {
			return err
		}
		return fmt.Errorf("chat not found")
	}
	// v2 structure/indexing is independent of AI; do not generate or rewrite KB.
	if queryObjectsEnabled(chat) {
		return nil
	}
	stored, readErr := s.kbRepo.FindByChatID(ctx, chatObjID)
	if readErr != nil {
		return readErr
	}
	if stored != nil && stored.KnowledgeVersion >= 2 {
		return nil
	}
	if stored != nil && !stored.UserID.IsZero() && stored.UserID != chat.UserID {
		return ErrObjectKnowledgeScope
	}
	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	if err := s.sourcePolicy().RequireRead(ctx, chat.UserID, chat, connection); err != nil {
		return err
	}

	// Generate descriptions via LLM
	descriptions, err := s.generateKBDescriptionsViaLLM(ctx, formattedSchema, chat.PreferredLLMModel)
	if err != nil {
		log.Printf("ChatService -> syncKnowledgeBase -> LLM description generation failed: %v", err)
		return err
	}

	if len(descriptions) == 0 {
		log.Printf("ChatService -> syncKnowledgeBase -> LLM returned no descriptions, skipping")
		return nil
	}

	// Get or create KB
	kb, err := s.kbRepo.FindByChatID(ctx, chatObjID)
	if err != nil {
		log.Printf("ChatService -> syncKnowledgeBase -> Error fetching KB: %v", err)
		return err
	}
	if kb == nil {
		kb = models.NewKnowledgeBase(chatObjID)
	}
	kb.UserID = chat.UserID

	// Merge: keep any user-edited descriptions, fill in LLM-generated ones for empty fields
	existingMap := make(map[string]*models.TableDescription)
	for i := range kb.TableDescriptions {
		existingMap[kb.TableDescriptions[i].TableName] = &kb.TableDescriptions[i]
	}

	generatedAt := time.Now().UTC().Truncate(time.Millisecond)
	for _, genTD := range descriptions {
		genTD.Provenance = models.KnowledgeAIGenerated
		genTD.UpdatedAt = generatedAt
		for i := range genTD.FieldDescriptions {
			genTD.FieldDescriptions[i].Provenance = models.KnowledgeAIGenerated
			genTD.FieldDescriptions[i].UpdatedAt = generatedAt
		}
		existing, has := existingMap[genTD.TableName]
		if !has {
			// New table — use LLM description as-is
			kb.TableDescriptions = append(kb.TableDescriptions, genTD)
			continue
		}
		// Table exists — only fill in empty descriptions (don't overwrite user edits)
		if existing.Description == "" {
			existing.Description = genTD.Description
			existing.Provenance = models.KnowledgeAIGenerated
			existing.UpdatedAt = generatedAt
		}
		// Merge field descriptions
		existingFieldMap := make(map[string]*models.FieldDescription)
		for j := range existing.FieldDescriptions {
			existingFieldMap[existing.FieldDescriptions[j].FieldName] = &existing.FieldDescriptions[j]
		}
		for _, genFD := range genTD.FieldDescriptions {
			if existingFD, ok := existingFieldMap[genFD.FieldName]; ok {
				if existingFD.Description == "" {
					existingFD.Description = genFD.Description
					existingFD.Provenance = models.KnowledgeAIGenerated
					existingFD.UpdatedAt = generatedAt
				}
			} else {
				existing.FieldDescriptions = append(existing.FieldDescriptions, genFD)
			}
		}
	}

	// Save to MongoDB
	if err := s.kbRepo.Upsert(ctx, kb); err != nil {
		log.Printf("ChatService -> syncKnowledgeBase -> Error saving KB: %v", err)
		return err
	}

	log.Printf("ChatService -> syncKnowledgeBase -> KB sync completed for chatID: %s (%d table descriptions saved)", chatID, len(kb.TableDescriptions))
	return nil
}

// generateKBDescriptionsViaLLM calls the LLM with the formatted schema to enrich the knowledge base descriptions.
// It uses GenerateRawJSON to get the LLM to return raw KB JSON without the standard YanfaChatBI response schema, which allows for more flexible output.
func (s *chatService) generateKBDescriptionsViaLLM(ctx context.Context, formattedSchema string, preferredModel *string) ([]models.TableDescription, error) {
	// Resolve LLM client
	llmClient := s.llmClient
	modelID := ""
	if preferredModel != nil && *preferredModel != "" {
		modelID = *preferredModel
		if providerClient, rawModel, ok := s.resolveLLMSelection(ctx, modelID); ok {
			llmClient = providerClient
			modelID = rawModel
		}
	}

	if llmClient == nil {
		return nil, fmt.Errorf("no LLM client available")
	}

	// Trim the schema for KB generation: strip long example values, deeply nested fields,
	// and sensitive data to drastically reduce the token count and speed up the LLM call.
	trimmedSchema := trimSchemaForKB(formattedSchema)
	log.Printf("ChatService -> generateKBDescriptionsViaLLM -> Schema trimmed from %d to %d chars (%.1f%% reduction)",
		len(formattedSchema), len(trimmedSchema), (1-float64(len(trimmedSchema))/float64(len(formattedSchema)))*100)

	// Build the user message: KB prompt + trimmed schema
	kbUserMessage := constants.KBDescriptionGenerationPrompt + "\n\nHere is the database schema:\n\n" + trimmedSchema

	// Use GenerateRawJSON (not GenerateResponse) — this bypasses the standard YanfaChatBI
	// response schema so the LLM returns raw KB JSON instead of assistantMessage/queries format.
	response, err := llmClient.GenerateRawJSON(ctx, constants.KBDescriptionGenerationPrompt, kbUserMessage, modelID)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %v", err)
	}

	log.Printf("ChatService -> generateKBDescriptionsViaLLM -> Raw response length: %d", len(response))

	// The response should be raw KB JSON from GenerateRawJSON.
	// Try to extract JSON from the text (may contain markdown code blocks).
	kbJSON := extractJSONFromText(response)

	// Fallback: if the response was wrapped in an assistantMessage envelope (e.g. if the
	// LLM still returned the standard format), extract the inner JSON.
	var llmResp map[string]interface{}
	if err := json.Unmarshal([]byte(kbJSON), &llmResp); err == nil {
		if msg, ok := llmResp["assistantMessage"].(string); ok && msg != "" {
			kbJSON = extractJSONFromText(msg)
		}
	}

	// Parse the KB descriptions
	var kbResp struct {
		Tables []struct {
			TableName         string `json:"table_name"`
			Description       string `json:"description"`
			FieldDescriptions []struct {
				FieldName   string `json:"field_name"`
				Description string `json:"description"`
			} `json:"field_descriptions"`
		} `json:"tables"`
	}

	if err := json.Unmarshal([]byte(kbJSON), &kbResp); err != nil {
		return nil, fmt.Errorf("failed to parse KB descriptions JSON: %v", err)
	}

	// Convert to models
	result := make([]models.TableDescription, 0, len(kbResp.Tables))
	for _, t := range kbResp.Tables {
		td := models.TableDescription{
			TableName:   t.TableName,
			Description: t.Description,
		}
		for _, f := range t.FieldDescriptions {
			td.FieldDescriptions = append(td.FieldDescriptions, models.FieldDescription{
				FieldName:   f.FieldName,
				Description: f.Description,
			})
		}
		result = append(result, td)
	}

	log.Printf("ChatService -> generateKBDescriptionsViaLLM -> Generated descriptions for %d tables", len(result))
	return result, nil
}

// trimSchemaForKB reduces the formatted schema size for KB generation by:
// 1. Truncating long example record values to short previews
// 2. Removing deeply nested fields (3+ levels) from the column listing
// 3. Stripping sensitive-looking data (tokens, passwords, encrypted values)
// 4. Limiting example records to 1 per table
// This can reduce a 1.3M schema to ~100-200K, cutting LLM response time by 5-10x.
func trimSchemaForKB(schema string) string {
	lines := strings.Split(schema, "\n")
	var result strings.Builder
	result.Grow(len(schema) / 4) // Pre-allocate ~25% of original

	inExampleRecords := false
	recordCount := 0
	skipUntilNextSection := false

	// Regex patterns for sensitive data
	sensitivePatterns := regexp.MustCompile(`(?i)(token|password|secret|apikey|api_key|refresh_token|access_token|google_access|google_refresh|ssl_cert|ssl_key|ssl_root)`)
	encryptedPattern := regexp.MustCompile(`^ENC:[A-Za-z0-9+/=]+$`)
	base64LikePattern := regexp.MustCompile(`^[A-Za-z0-9+/]{40,}={0,2}$`)

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Detect example records section
		if trimmedLine == "Example Records:" {
			inExampleRecords = true
			recordCount = 0
			skipUntilNextSection = false
			result.WriteString(line)
			result.WriteString("\n")
			continue
		}

		// Detect start of a new record
		if inExampleRecords && strings.HasPrefix(trimmedLine, "Record ") && strings.HasSuffix(trimmedLine, ":") {
			recordCount++
			if recordCount > 1 {
				// Skip records after the first one
				skipUntilNextSection = true
				continue
			}
			skipUntilNextSection = false
			result.WriteString(line)
			result.WriteString("\n")
			continue
		}

		// Detect end of example records (new table or section)
		if inExampleRecords && (strings.HasPrefix(trimmedLine, "Table:") ||
			strings.HasPrefix(trimmedLine, "Row Count:") ||
			strings.HasPrefix(trimmedLine, "View:") ||
			(trimmedLine == "" && skipUntilNextSection)) {
			if strings.HasPrefix(trimmedLine, "Table:") || strings.HasPrefix(trimmedLine, "View:") {
				inExampleRecords = false
				skipUntilNextSection = false
			}
			if skipUntilNextSection && trimmedLine == "" {
				continue // Skip blank lines between skipped records
			}
			result.WriteString(line)
			result.WriteString("\n")
			continue
		}

		if skipUntilNextSection {
			continue
		}

		// Skip deeply nested field definitions (3+ dots = 3+ nesting levels)
		// e.g. "  - candidate.resume.education.location.city (String)" → skip
		if strings.HasPrefix(trimmedLine, "- ") && strings.Contains(trimmedLine, "(") {
			fieldPart := strings.TrimPrefix(trimmedLine, "- ")
			fieldName := strings.Split(fieldPart, " ")[0]
			if strings.Count(fieldName, ".") >= 3 {
				continue // Skip deeply nested fields
			}
		}

		// Inside example records: truncate long values
		if inExampleRecords && strings.Contains(line, ": ") {
			parts := strings.SplitN(line, ": ", 2)
			if len(parts) == 2 {
				key := parts[0]
				value := parts[1]
				fieldName := strings.TrimSpace(key)

				// Strip sensitive fields entirely
				if sensitivePatterns.MatchString(fieldName) {
					result.WriteString(key)
					result.WriteString(": \"[REDACTED]\"\n")
					continue
				}

				// Strip encrypted/base64 values
				cleanVal := strings.Trim(strings.TrimSpace(value), "\"")
				if encryptedPattern.MatchString(cleanVal) || base64LikePattern.MatchString(cleanVal) {
					result.WriteString(key)
					result.WriteString(": \"[ENCRYPTED]\"\n")
					continue
				}

				// Truncate long string values (>200 chars)
				if len(value) > 200 {
					result.WriteString(key)
					result.WriteString(": ")
					result.WriteString(value[:200])
					result.WriteString("...[truncated]\"\n")
					continue
				}
			}
		}

		result.WriteString(line)
		result.WriteString("\n")
	}

	return result.String()
}

// extractJSONFromText tries to extract a JSON object from text that may contain
// markdown code blocks or other wrapping.
func extractJSONFromText(text string) string {
	// Try as-is first
	text = strings.TrimSpace(text)
	if len(text) > 0 && text[0] == '{' {
		return text
	}

	// Look for ```json ... ``` blocks
	if idx := strings.Index(text, "```json"); idx >= 0 {
		start := idx + 7
		if end := strings.Index(text[start:], "```"); end >= 0 {
			return strings.TrimSpace(text[start : start+end])
		}
	}

	// Look for ``` ... ``` blocks
	if idx := strings.Index(text, "```"); idx >= 0 {
		start := idx + 3
		// Skip to the next line
		for start < len(text) && text[start] != '\n' {
			start++
		}
		if start < len(text) {
			start++
		}
		if end := strings.Index(text[start:], "```"); end >= 0 {
			return strings.TrimSpace(text[start : start+end])
		}
	}

	// Look for first { and try to extract balanced JSON
	if idx := strings.Index(text, "{"); idx >= 0 {
		return text[idx:]
	}

	return text
}
