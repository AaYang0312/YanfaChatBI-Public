package handlers

import (
	"github.com/gin-gonic/gin"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/services"
	"net/http"
	"net/http/httptest"
	"testing"
)

type scopedDuplicateService struct {
	services.ChatService
	selection *string
	called    bool
	messages  bool
}

func (s *scopedDuplicateService) Duplicate(_, _ string, messages, _ bool, selection ...*string) (*dtos.ChatResponse, uint32, error) {
	s.called = true
	s.messages = messages
	if len(selection) > 0 {
		s.selection = selection[0]
	}
	return &dtos.ChatResponse{}, http.StatusOK, nil
}
func TestScopedDuplicateQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		query   string
		present bool
		value   string
		status  int
	}{
		{"", false, "", 200}, {"?duplicate_messages=false&selected_collections=bi.sales", true, "bi.sales", 200},
		{"?selected_collections=", false, "", 400}, {"?selected_collections=a&selected_collections=b", false, "", 400},
		{"?selected_collections=%", false, "", 400},
		{"?selected_collections=%ZZ", false, "", 400},
		{"?selected_collections=bi.sales&selected_collections=%ZZ", false, "", 400},
		{"?selected_collections=bi.sales;bi.items", false, "", 400},
		{"?selected_collections=bi.sales&other=%ZZ", false, "", 400},
		{"?selected_collections=%20", false, "", 400},
	} {
		t.Run(tt.query, func(t *testing.T) {
			s := &scopedDuplicateService{}
			h := &ChatHandler{chatService: s}
			router := gin.New()
			router.POST("/chats/:id/duplicate", h.Duplicate)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/chats/test/duplicate"+tt.query, nil))
			if w.Code != tt.status {
				t.Fatalf("status %d", w.Code)
			}
			if tt.status == 400 {
				if s.called {
					t.Fatal("ambiguous selection reached service")
				}
				return
			}
			if !s.called || s.messages || (s.selection != nil) != tt.present {
				t.Fatal("query semantics changed")
			}
			if tt.present && *s.selection != tt.value {
				t.Fatalf("selection=%q", *s.selection)
			}
		})
	}
}
