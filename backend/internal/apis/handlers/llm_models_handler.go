package handlers

import (
	"net/http"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/services"

	"github.com/gin-gonic/gin"
)

// LLMModelsHandler exposes the aggregated model catalogue built from the
// configured providers.
type LLMModelsHandler struct {
	providers *services.ModelProviderService
}

// NewLLMModelsHandler wires the provider service.
func NewLLMModelsHandler(providers *services.ModelProviderService) *LLMModelsHandler {
	return &LLMModelsHandler{providers: providers}
}

// LLMModelView is the catalogue entry consumed by the chat model selector.
type LLMModelView struct {
	ID                  string                `json:"id"`
	Provider            string                `json:"provider"`
	ProviderName        string                `json:"providerName"`
	DisplayName         string                `json:"displayName"`
	IsEnabled           bool                  `json:"isEnabled"`
	Default             bool                  `json:"default"`
	MaxCompletionTokens int                   `json:"maxCompletionTokens"`
	InputTokenLimit     int                   `json:"inputTokenLimit"`
	Temperature         float64               `json:"temperature"`
	Reasoning           *models.ReasoningSpec `json:"reasoning,omitempty"`
}

// catalogue builds the flat model list from enabled providers.
func (h *LLMModelsHandler) catalogue(c *gin.Context) ([]LLMModelView, error) {
	providers, err := h.providers.List(c.Request.Context())
	if err != nil {
		return nil, err
	}
	out := make([]LLMModelView, 0)
	first := true
	for _, provider := range providers {
		if !provider.Enabled {
			continue
		}
		for i := range provider.Models {
			model := &provider.Models[i]
			displayName := model.DisplayName
			if displayName == "" {
				displayName = model.ID
			}
			out = append(out, LLMModelView{
				ID:                  provider.Slug + "/" + model.ID,
				Provider:            provider.Slug,
				ProviderName:        provider.Name,
				DisplayName:         displayName,
				IsEnabled:           true,
				Default:             first,
				MaxCompletionTokens: model.MaxCompletionTokens,
				InputTokenLimit:     model.ContextLimit,
				Temperature:         model.Temperature,
				Reasoning:           model.Reasoning,
			})
			first = false
		}
	}
	return out, nil
}

// GetSupportedModels returns all enabled models across providers.
func (h *LLMModelsHandler) GetSupportedModels(c *gin.Context) {
	modelsList, err := h.catalogue(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "failed to load model catalogue"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"models": modelsList, "count": len(modelsList)}})
}

// GetModelsByProvider filters the catalogue by provider slug.
func (h *LLMModelsHandler) GetModelsByProvider(c *gin.Context) {
	provider := c.Param("provider")
	modelsList, err := h.catalogue(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "failed to load model catalogue"})
		return
	}
	filtered := make([]LLMModelView, 0)
	for _, model := range modelsList {
		if model.Provider == provider {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "provider not configured: " + provider})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"provider": provider, "models": filtered, "count": len(filtered)}})
}

// GetModelDetails returns one catalogue entry by composite id.
func (h *LLMModelsHandler) GetModelDetails(c *gin.Context) {
	modelID := c.Param("modelId")
	modelsList, err := h.catalogue(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "failed to load model catalogue"})
		return
	}
	for _, model := range modelsList {
		if model.ID == modelID {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": model})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Model not found"})
}

// GetDefaultModel returns the first catalogue entry (the deployment default).
func (h *LLMModelsHandler) GetDefaultModel(c *gin.Context) {
	modelsList, err := h.catalogue(c)
	if err != nil || len(modelsList) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "no model configured"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"default_model": modelsList[0].ID,
		"model_details": modelsList[0],
	}})
}
