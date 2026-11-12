package routes

import (
	"github.com/gin-gonic/gin"
	"log"
	"yanfachatbi/internal/apis/middlewares"
	"yanfachatbi/internal/di"
)

func SetupDataSyncRoutes(router *gin.Engine) {
	handler, err := di.GetDataSyncHandler()
	if err != nil {
		log.Fatal("Data sync HTTP dependencies unavailable")
	}
	protected := router.Group("/api/chats")
	protected.Use(middlewares.AuthMiddleware())
	protected.GET("/:id/data-sync/status", handler.Status)
	protected.POST("/:id/data-sync/jobs", handler.CreateJob)
	protected.GET("/:id/data-sync/jobs/:jobID", handler.GetJob)
}
