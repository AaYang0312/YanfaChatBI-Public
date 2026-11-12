package services

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"testing"
	"time"
)

type scopedKnowledgeVectors struct {
	VectorizationService
	copies chan bool
}

func (v *scopedKnowledgeVectors) IsAvailable(context.Context) bool { return true }
func (v *scopedKnowledgeVectors) CopyVectorsForChat(_ context.Context, _, _ string, messages bool, _ map[string]string) error {
	v.copies <- messages
	return nil
}

type scopedKnowledgeRepo struct {
	repositories.KnowledgeBaseRepository
	reads chan bool
}

func (r *scopedKnowledgeRepo) FindByChatID(context.Context, primitive.ObjectID) (*models.KnowledgeBase, error) {
	r.reads <- true
	return nil, nil
}

type scopedKnowledgeChats struct{ *scopedChatRepo }

func (r *scopedKnowledgeChats) FindMessagesByChat(primitive.ObjectID, int, int) ([]*models.Message, int64, error) {
	return nil, 0, nil
}

func TestScopedDuplicateDoesNotInheritKnowledge(t *testing.T) {
	for _, messages := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("messages=%v/explicit=%v", messages, explicit), func(t *testing.T) {
				s, r := scopedChatService(t)
				s.chatRepo = &scopedKnowledgeChats{r}
				owner := primitive.NewObjectID()
				r.original = models.NewChat(owner, models.Connection{Type: "postgresql", Host: "fixture", Database: "fixture"}, models.DefaultChatSettings())
				// Identical explicit scope must also avoid inherited knowledge.
				selection := "bi.sales"
				r.original.SelectedCollections = selection
				vectors := &scopedKnowledgeVectors{copies: make(chan bool, 1)}
				kb := &scopedKnowledgeRepo{reads: make(chan bool, 1)}
				s.vectorizationSvc = vectors
				s.kbRepo = kb
				var scope *string
				if explicit {
					scope = &selection
				}
				if _, _, err := s.Duplicate(owner.Hex(), r.original.ID.Hex(), messages, false, scope); err != nil {
					t.Fatal(err)
				}
				if explicit {
					select {
					case <-vectors.copies:
						t.Fatal("explicit scope copied old schema vectors")
					case <-kb.reads:
						t.Fatal("explicit scope read old knowledge base for copying")
					case <-time.After(100 * time.Millisecond):
					}
				} else {
					select {
					case got := <-vectors.copies:
						if got != messages {
							t.Fatal("legacy message vector behavior changed")
						}
					case <-time.After(time.Second):
						t.Fatal("omitted scope no longer copies vectors")
					}
					select {
					case <-kb.reads:
					case <-time.After(time.Second):
						t.Fatal("omitted scope no longer copies knowledge base")
					}
				}
			})
		}
	}
}
