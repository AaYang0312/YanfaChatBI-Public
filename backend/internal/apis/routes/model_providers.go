package routes

import (
	"log"

	"yanfachatbi/internal/apis/middlewares"
	"yanfachatbi/internal/di"

	"github.com/gin-gonic/gin"
)

// SetupModelProviderRoutes exposes admin-only provider management. Every
// mutation is guarded by the handler's admin check, and the group requires a
// valid session.
func SetupModelProviderRoutes(router *gin.Engine) {
	handler, err := di.GetModelProviderHandler()
	if err != nil {
		log.Fatalf("Failed to get model provider handler: %v", err)
	}
	group := router.Group("/api/model-providers")
	group.Use(middlewares.AuthMiddleware())
	{
		group.GET("", handler.List)
		group.POST("", handler.Create)
		group.PUT("/:id", handler.Update)
		group.DELETE("/:id", handler.Delete)
		group.POST("/probe", handler.Probe)
		group.POST("/:id/refresh-models", handler.RefreshModels)
	}
	log.Println("Model provider routes set up successfully")
}
