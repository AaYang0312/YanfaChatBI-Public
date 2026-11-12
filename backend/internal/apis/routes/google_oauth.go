package routes

import (
	"yanfachatbi/internal/apis/handlers"
	"yanfachatbi/internal/apis/middlewares"

	"github.com/gin-gonic/gin"
)

func SetupGoogleOAuthRoutes(router *gin.Engine) {
	googleHandler := handlers.NewGoogleOAuthHandler()

	googleGroup := router.Group("/api/google")
	{
		// Public endpoints for OAuth flow
		googleGroup.GET("/auth", googleHandler.InitiateGoogleAuth)
		googleGroup.GET("/callback", googleHandler.HandleGoogleCallback)

		// Protected endpoints requiring authentication
		protectedGroup := googleGroup.Group("")
		protectedGroup.Use(middlewares.AuthMiddleware())
		{
			protectedGroup.POST("/validate-sheet", googleHandler.ValidateGoogleSheetAccess)
			protectedGroup.POST("/refresh-token", googleHandler.RefreshGoogleToken)
		}
	}
}
