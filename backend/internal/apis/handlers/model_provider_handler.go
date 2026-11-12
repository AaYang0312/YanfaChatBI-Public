package handlers

import (
	"errors"
	"net/http"

	"yanfachatbi/config"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ModelProviderHandler exposes admin-only provider management.
type ModelProviderHandler struct {
	service *services.ModelProviderService
	users   repositories.UserRepository
}

// NewModelProviderHandler builds the handler with its service and user lookup.
func NewModelProviderHandler(service *services.ModelProviderService, users repositories.UserRepository) *ModelProviderHandler {
	return &ModelProviderHandler{service: service, users: users}
}

// isAdmin matches the deployment's single admin identity used at login.
func (h *ModelProviderHandler) isAdmin(c *gin.Context) bool {
	userID := c.GetString("userID")
	if userID == "" || h.users == nil {
		return false
	}
	user, err := h.users.FindByID(userID)
	if err != nil || user == nil {
		return false
	}
	return user.Username == config.Env.AdminUser
}

func (h *ModelProviderHandler) requireAdmin(c *gin.Context) bool {
	if !h.isAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "error": "admin privileges required"})
		return false
	}
	return true
}

// List returns configured providers with masked keys (admin only).
func (h *ModelProviderHandler) List(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	providers, err := h.service.List(c.Request.Context())
	if err != nil {
		writeModelProviderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"providers": providers, "count": len(providers)}})
}

// Create adds a provider; refresh_models=true asks for a catalogue fetch.
func (h *ModelProviderHandler) Create(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	var input services.ProviderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid provider payload"})
		return
	}
	provider, err := h.service.Create(c.Request.Context(), input)
	if err != nil {
		writeModelProviderError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": provider})
}

// Update modifies a provider; an empty api_key keeps the stored one.
func (h *ModelProviderHandler) Update(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid provider id"})
		return
	}
	var input services.ProviderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid provider payload"})
		return
	}
	provider, err := h.service.Update(c.Request.Context(), id, input)
	if err != nil {
		writeModelProviderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": provider})
}

// Delete removes a provider.
func (h *ModelProviderHandler) Delete(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid provider id"})
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		writeModelProviderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// Probe fetches {base}/models for a candidate endpoint without saving it.
func (h *ModelProviderHandler) Probe(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	var input struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid probe payload"})
		return
	}
	modelsList, err := h.service.Probe(c.Request.Context(), input.BaseURL, input.APIKey)
	if err != nil {
		writeModelProviderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"models": modelsList, "count": len(modelsList)}})
}

// RefreshModels re-reads the catalogue for a stored provider.
func (h *ModelProviderHandler) RefreshModels(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid provider id"})
		return
	}
	provider, err := h.service.RefreshModels(c.Request.Context(), id)
	if err != nil {
		writeModelProviderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": provider})
}

func writeModelProviderError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, repositories.ErrModelProviderNotFound):
		status = http.StatusNotFound
	case errors.Is(err, repositories.ErrModelProviderConflict):
		status = http.StatusConflict
	case errors.Is(err, services.ErrModelProviderInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, services.ErrModelProviderUnauthorized):
		status = http.StatusBadRequest
	case errors.Is(err, services.ErrModelProviderModelsUnsupported):
		status = http.StatusBadRequest
	case errors.Is(err, services.ErrModelProviderProbeTimeout):
		status = http.StatusGatewayTimeout
	case errors.Is(err, services.ErrModelProviderProbeInvalid):
		status = http.StatusBadGateway
	case errors.Is(err, services.ErrModelProviderKeyUnconfigured):
		status = http.StatusInternalServerError
	}
	c.JSON(status, gin.H{"success": false, "error": err.Error()})
}
