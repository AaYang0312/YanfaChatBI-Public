package routes

import (
	"log"
	"yanfachatbi/internal/apis/middlewares"
	"yanfachatbi/internal/di"

	"github.com/gin-gonic/gin"
)

func SetupVisualizationRoutes(router *gin.Engine) {
	vizHandler, err := di.GetVisualizationHandler()
	if err != nil {
		log.Fatalf("Failed to get visualization handler: %v", err)
	}

	protected := router.Group("/api/chats")
	protected.Use(middlewares.AuthMiddleware())
	{
		// Visualization routes
		protected.POST("/:id/messages/:messageId/visualizations", vizHandler.GenerateVisualization)
		protected.POST("/:id/execute-chart", vizHandler.ExecuteChartQuery)
		protected.POST("/:id/visualization-data", vizHandler.GetVisualizationData)
	}
}
