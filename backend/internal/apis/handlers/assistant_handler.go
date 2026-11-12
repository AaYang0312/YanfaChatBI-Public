package handlers

import (
	"io"
	"net/http"
	"strings"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/services"

	"github.com/gin-gonic/gin"
)

// AssistantHandler exposes resolve, create, get, and cancel. The owner always
// comes from the authenticated userID; request bodies cannot set it.
type AssistantHandler struct {
	service services.AssistantService
}

// NewAssistantHandler binds the assistant service.
func NewAssistantHandler(service services.AssistantService) *AssistantHandler {
	return &AssistantHandler{service: service}
}

// Resolve handles POST /api/chats/:id/assistant/resolve.
func (h *AssistantHandler) Resolve(c *gin.Context) {
	access, ok := assistantAccess(c)
	if !ok {
		return
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		assistantWriteError(c, models.NewValidationError(models.ErrorCodeInvalidSchema, "", "draft body is empty"))
		return
	}
	wire, err := dtos.DecodeAssistantDraftRequest(raw)
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	draft, err := wire.ToDomain()
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	resolution, err := h.service.Resolve(c.Request.Context(), access, draft)
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, dtos.Response{Success: true, Data: dtos.NewResolution(resolution)})
}

// CreateRun handles POST /api/chats/:id/assistant/runs.
func (h *AssistantHandler) CreateRun(c *gin.Context) {
	access, ok := assistantAccess(c)
	if !ok {
		return
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		assistantWriteError(c, models.NewValidationError(models.ErrorCodeInvalidSchema, "", "analysis request body is empty"))
		return
	}
	wire, err := dtos.DecodeAssistantAnalysisRequest(raw)
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	req, err := wire.ToDomain()
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	view, err := h.service.CreateRun(c.Request.Context(), access, req)
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, dtos.Response{Success: true, Data: assistantAccepted(view)})
}

// GetRun handles GET /api/chats/:id/assistant/runs/:runID.
func (h *AssistantHandler) GetRun(c *gin.Context) {
	access, ok := assistantAccess(c)
	if !ok {
		return
	}
	view, err := h.service.GetRun(c.Request.Context(), access, c.Param("runID"))
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, dtos.Response{Success: true, Data: dtos.NewRunView(view)})
}

// CancelRun handles POST /api/chats/:id/assistant/runs/:runID/cancel.
func (h *AssistantHandler) CancelRun(c *gin.Context) {
	access, ok := assistantAccess(c)
	if !ok {
		return
	}
	view, err := h.service.CancelRun(c.Request.Context(), access, c.Param("runID"))
	if err != nil {
		assistantWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, dtos.Response{Success: true, Data: dtos.NewRunView(view)})
}

// assistantAccess builds AccessContext from the authenticated user and the
// chat path. Client JSON is never decoded into this value.
func assistantAccess(c *gin.Context) (models.AccessContext, bool) {
	ownerID := strings.TrimSpace(c.GetString("userID"))
	chatID := strings.TrimSpace(c.Param("id"))
	if ownerID == "" {
		err := "Unauthorized"
		c.JSON(http.StatusUnauthorized, dtos.Response{Success: false, Error: &err})
		return models.AccessContext{}, false
	}
	if chatID == "" {
		assistantWriteError(c, services.AssistantRunNotFoundError{})
		return models.AccessContext{}, false
	}
	return models.AccessContext{OwnerID: ownerID, ChatID: chatID}, true
}

func assistantAccepted(view models.RunView) gin.H {
	return gin.H{
		"run_id":     view.RunID,
		"message_id": view.MessageID,
		"status":     view.Status,
	}
}

func assistantWriteError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if writeSourceAccessError(c, err) {
		return
	}
	if services.IsAssistantNotFound(err) {
		message := "Not found"
		c.JSON(http.StatusNotFound, dtos.Response{Success: false, Error: &message})
		return
	}
	if validation, ok := models.AsValidationError(err); ok {
		status, mapped := validation.Code.HTTPStatus()
		if !mapped {
			status = http.StatusUnprocessableEntity
		}
		message := validation.Error()
		c.JSON(status, dtos.Response{Success: false, Error: &message, Data: validation})
		return
	}
	message := "Internal server error"
	c.JSON(http.StatusInternalServerError, dtos.Response{Success: false, Error: &message})
}
