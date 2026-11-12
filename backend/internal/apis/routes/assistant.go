package routes

import (
	"log"

	"yanfachatbi/internal/apis/middlewares"
	"yanfachatbi/internal/di"

	"github.com/gin-gonic/gin"
)

// SetupAssistantRoutes registers resolve, create, get, and cancel on its own
// authenticated /api/chats group. Owner comes from AuthMiddleware, not the body.
func SetupAssistantRoutes(router *gin.Engine) {
	handler, err := di.GetAssistantHandler()
	if err != nil {
		log.Fatalf("Failed to get assistant handler: %v", err)
	}
	protected := router.Group("/api/chats")
	protected.Use(middlewares.AuthMiddleware())
	{
		protected.POST("/:id/assistant/resolve", handler.Resolve)
		protected.POST("/:id/assistant/runs", handler.CreateRun)
		protected.GET("/:id/assistant/runs/:runID", handler.GetRun)
		protected.POST("/:id/assistant/runs/:runID/cancel", handler.CancelRun)
	}
}
