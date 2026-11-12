package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"net/http"
	"testing"
)

type scopedChatRepo struct {
	repositories.ChatRepository
	original *models.Chat
	created  *models.Chat
}

func (r *scopedChatRepo) Create(c *models.Chat) error { copy := *c; r.created = &copy; return nil }
func (r *scopedChatRepo) FindByID(id primitive.ObjectID) (*models.Chat, error) {
	return r.original, nil
}
func scopedChatService(t *testing.T) (*chatService, *scopedChatRepo) {
	t.Helper()
	oldMax, oldKey := config.Env.MaxChatsPerUser, config.Env.SchemaEncryptionKey
	config.Env.MaxChatsPerUser = 100
	config.Env.SchemaEncryptionKey = "0123456789abcdef0123456789abcdef"
	t.Cleanup(func() { config.Env.MaxChatsPerUser = oldMax; config.Env.SchemaEncryptionKey = oldKey })
	r := &scopedChatRepo{}
	return &chatService{chatRepo: r, sourceAccess: NewUnconfiguredSourceAccessService(r)}, r
}
func TestScopedChatCreateBeforePersistence(t *testing.T) {
	for _, tt := range []struct{ field, want string }{
		{``, "ALL"}, {`,"selected_collections":" bi.sales,bi.items "`, "bi.sales,bi.items"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			s, r := scopedChatService(t)
			var req dtos.CreateChatRequest
			if err := json.Unmarshal([]byte(`{"connection":{"type":"postgresql","host":"fixture","database":"fixture"}`+tt.field+`}`), &req); err != nil {
				t.Fatal(err)
			}
			out, status, err := s.CreateWithoutConnectionPing(primitive.NewObjectID().Hex(), &req)
			if err != nil || status != http.StatusCreated {
				t.Fatalf("status=%d err=%v", status, err)
			}
			if r.created.SelectedCollections != tt.want || out.SelectedCollections != tt.want {
				t.Fatalf("scope at first persistence = %q, response = %q", r.created.SelectedCollections, out.SelectedCollections)
			}
		})
	}
}

func TestScopedChatInvalidCreateNeverPersists(t *testing.T) {
	for _, value := range []string{"", " ", ",", "bi.sales,", "ALL,bi.sales", "bi.*", "bi.sales;DROP", "bi.sales,bi.sales", "bi..sales"} {
		t.Run(value, func(t *testing.T) {
			s, r := scopedChatService(t)
			req := &dtos.CreateChatRequest{SelectedCollections: &value}
			_, status, err := s.CreateWithoutConnectionPing(primitive.NewObjectID().Hex(), req)
			if err == nil || status != http.StatusBadRequest || r.created != nil {
				t.Fatalf("invalid selection persisted: status=%d err=%v", status, err)
			}
		})
	}
}

func TestScopedChatDuplicateSelectionAndNoHistory(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprint(override), func(t *testing.T) {
			s, r := scopedChatService(t)
			owner := primitive.NewObjectID()
			r.original = models.NewChat(owner, models.Connection{Type: "postgresql", Host: "fixture", Database: "fixture"}, models.DefaultChatSettings())
			r.original.SelectedCollections = "ALL"
			staleSchema := "unscoped original schema"
			r.original.Connection.CurrentSchema = &staleSchema
			// All message/dashboard repository methods are nil: any history read fails this test.
			selection := "bi.sales"
			var out *dtos.ChatResponse
			var status uint32
			var err error
			want := "ALL"
			if override {
				out, status, err = s.Duplicate(owner.Hex(), r.original.ID.Hex(), false, false, &selection)
				want = selection
			} else {
				out, status, err = s.Duplicate(owner.Hex(), r.original.ID.Hex(), false, false)
			}
			if err != nil || status != http.StatusOK {
				t.Fatalf("duplicate status=%d err=%v", status, err)
			}
			if r.created.SelectedCollections != want || out.SelectedCollections != want || r.created.ID == r.original.ID {
				t.Fatal("new chat scope/identity incorrect")
			}
			if override && r.created.Connection.CurrentSchema != nil {
				t.Fatal("explicit scope inherited stale schema")
			}
			if r.original.SelectedCollections != "ALL" {
				t.Fatal("original chat changed")
			}
		})
	}
}

func TestScopedChatDuplicateDoesNotBypassSourceGate(t *testing.T) {
	s, r := scopedChatService(t)
	owner := primitive.NewObjectID()
	r.original = models.NewChat(owner, models.Connection{Type: "postgresql", Host: "fixture", Database: "fixture"}, models.DefaultChatSettings())
	r.original.ERPAssociated = true
	r.original.SelectedCollections = "ALL"
	selection := "bi.sales"
	out, _, err := s.Duplicate(owner.Hex(), r.original.ID.Hex(), false, false, &selection)
	if out != nil || !errors.Is(err, ErrSourceUnresolved) {
		t.Fatalf("unresolved ERP admitted: %v", err)
	}
	if r.created == nil || r.created.SelectedCollections != selection || r.created.SourceBindingConfirmed {
		t.Fatal("scope must precede source binding, and binding must remain unconfirmed")
	}
}

func TestScopedChatDuplicateInvalidSelectionNeverPersists(t *testing.T) {
	s, r := scopedChatService(t)
	owner := primitive.NewObjectID()
	r.original = models.NewChat(owner, models.Connection{}, models.DefaultChatSettings())
	empty := ""
	_, status, err := s.Duplicate(owner.Hex(), r.original.ID.Hex(), false, false, &empty)
	if status != http.StatusBadRequest || err == nil || r.created != nil {
		t.Fatalf("invalid duplicate accepted: %d %v", status, err)
	}
}

func (r *scopedChatRepo) ReadCurrent(_ context.Context, _ primitive.ObjectID, _ primitive.ObjectID) (*models.Chat, error) {
	return r.created, nil
}
func (r *scopedChatRepo) MarkPendingCAS(_ context.Context, _ *models.Chat, next *models.Chat) error {
	copy := *next
	r.created = &copy
	return nil
}
