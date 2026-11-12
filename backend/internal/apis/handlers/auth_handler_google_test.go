package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGoogleOAuthCallbackRejectsSignupBeforeCallingAuthService(t *testing.T) {
	// No auth service is installed: rejection must happen before any service call.
	handler := &AuthHandler{}
	router := gin.New()
	router.Use(gin.Recovery())
	router.POST("/auth/google/callback", handler.GoogleOAuthCallback)

	body := strings.NewReader(`{"code":"unused","redirect_uri":"http://localhost/auth/google/callback","purpose":"auth","action":"signup"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/google/callback", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", res.Code, res.Body.String())
	}
}
