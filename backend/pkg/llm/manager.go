package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

type Manager struct {
	clients     map[string]Client
	defaultName string
	mu          sync.RWMutex
}

func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]Client),
	}
}

// SetDefaultClient names the provider used when a chat has no explicit model.
func (m *Manager) SetDefaultClient(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultName = name
}

// DefaultClient returns the configured default provider client.
func (m *Manager) DefaultClient() (Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultName == "" {
		return nil, fmt.Errorf("no default LLM client configured")
	}
	client, exists := m.clients[m.defaultName]
	if !exists {
		return nil, fmt.Errorf("default LLM client not found: %s", m.defaultName)
	}
	return client, nil
}

func (m *Manager) RegisterClient(name string, config Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Every provider is OpenAI-compatible by contract; the provider string is
	// only an identity used for grouping and logging.
	client, err := NewOpenAIClient(config)
	if err != nil {
		return fmt.Errorf("failed to create LLM client: %v", err)
	}

	m.clients[name] = client
	return nil
}

// ReplaceClients atomically swaps the registered client set. The new map is
// built first, so a failure leaves the previous clients serving traffic.
func (m *Manager) ReplaceClients(next map[string]Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients = next
}

// Snapshot returns a copy of the current client registry for inspection.
func (m *Manager) Snapshot() map[string]Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]Client, len(m.clients))
	for name, client := range m.clients {
		out[name] = client
	}
	return out
}

func (m *Manager) GetClient(name string) (Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, exists := m.clients[name]
	if !exists {
		return nil, fmt.Errorf("LLM client not found: %s", name)
	}

	return client, nil
}

func (m *Manager) RemoveClient(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, name)
}

// Add helper function to properly format assistant response
func formatAssistantResponse(response map[string]interface{}) string {
	// Convert the response to JSON string
	jsonBytes, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		log.Printf("Error formatting assistant response: %v", err)
		return fmt.Sprintf("%v", response)
	}
	return string(jsonBytes)
}

// getAssistantContent safely extracts assistant response content from a message's
// Content map, handling both map[string]interface{} (normal JSON responses) and
// string (legacy/fallback format) types for the "assistant_response" key.
func getAssistantContent(content map[string]interface{}) string {
	if assistantMsg, ok := content["assistant_response"].(map[string]interface{}); ok {
		return formatAssistantResponse(assistantMsg)
	}
	if assistantStr, ok := content["assistant_response"].(string); ok {
		return assistantStr
	}
	return ""
}

// Helper functions
func mapRole(role string) string {
	switch strings.ToLower(role) {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	default:
		return "user"
	}
}
