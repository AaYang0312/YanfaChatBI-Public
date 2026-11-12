package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
)

// Only persistence is faked. These regressions call the real Update, including
// encryption, source classification and selection-vs-metadata dispatch. A
// spreadsheet connection avoids all network/probe/provider activity.
type legacyUpdateRepo struct {
	repositories.ChatRepository
	current                        models.Chat
	failCAS                        bool
	concurrentSelection            bool
	selectionPatches, atomicWrites int
}

func (r *legacyUpdateRepo) snapshot() *models.Chat {
	c := r.current
	c.Connection = r.current.Connection.Clone()
	return &c
}
func (r *legacyUpdateRepo) FindByID(primitive.ObjectID) (*models.Chat, error) {
	return r.snapshot(), nil
}
func (r *legacyUpdateRepo) ReadCurrent(context.Context, primitive.ObjectID, primitive.ObjectID) (*models.Chat, error) {
	return r.snapshot(), nil
}
func (r *legacyUpdateRepo) PatchMetadata(_ context.Context, owner, id primitive.ObjectID, p repositories.ChatMetadataPatch) error {
	if owner != r.current.UserID || id != r.current.ID {
		return repositories.ErrChatBindingConflict
	}
	if p.SelectedCollections != nil {
		if r.current.SelectedObjects != nil {
			return repositories.ErrObjectConflict
		}
		r.selectionPatches++
		r.current.SelectedCollections = *p.SelectedCollections
		r.current.SelectionRevision++
	}
	if p.DisplayName != nil {
		r.current.DisplayName = *p.DisplayName
	}
	return nil
}
func (r *legacyUpdateRepo) checkCAS(previous *models.Chat) error {
	if r.concurrentSelection {
		r.current.SelectedCollections = "concurrent_only"
		r.current.SelectionRevision++
		r.concurrentSelection = false
	}
	if r.failCAS || !sameChatBinding(previous, &r.current) || previous.SelectionRevision != r.current.SelectionRevision {
		return repositories.ErrChatBindingConflict
	}
	return nil
}
func (r *legacyUpdateRepo) UpdateIndependentConnectionCAS(_ context.Context, previous *models.Chat, connection models.Connection) error {
	if err := r.checkCAS(previous); err != nil {
		return err
	}
	r.current.Connection = connection.Clone()
	r.current.ConnectionRevision++
	return nil
}
func (r *legacyUpdateRepo) UpdateIndependentLegacyConnectionCAS(_ context.Context, previous, next *models.Chat) error {
	if err := r.checkCAS(previous); err != nil {
		return err
	}
	if previous.SelectedObjects != nil || next.SelectedObjects != nil || next.SelectionRevision != previous.SelectionRevision+1 {
		return repositories.ErrObjectConflict
	}
	r.current.Connection = next.Connection.Clone()
	r.current.Connection.CurrentSchema, r.current.Connection.SchemaUpdatedAt = nil, nil
	r.current.SelectedCollections = next.SelectedCollections
	r.current.SelectionRevision = next.SelectionRevision
	r.current.ConnectionRevision++
	r.current.ObjectState = nil
	r.atomicWrites++
	return nil
}
func (r *legacyUpdateRepo) MarkPendingCAS(_ context.Context, previous, next *models.Chat) error {
	if err := r.checkCAS(previous); err != nil {
		return err
	}
	r.current.SourceID = next.SourceID
	r.current.ERPAssociated = true
	r.current.SourceBindingRevision = next.SourceBindingRevision
	r.current.ConnectionRevision = next.ConnectionRevision
	r.current.SourceBindingConfirmed = false
	return nil
}
func newLegacyUpdateService(t *testing.T, enabled bool) (*chatService, *legacyUpdateRepo) {
	t.Helper()
	oldKey, oldFlag := config.Env.SchemaEncryptionKey, config.Env.QueryObjectsEnabled
	config.Env.SchemaEncryptionKey = "0123456789abcdef0123456789abcdef"
	config.Env.QueryObjectsEnabled = enabled
	t.Cleanup(func() { config.Env.SchemaEncryptionKey = oldKey; config.Env.QueryObjectsEnabled = oldFlag })
	chat := models.NewChat(primitive.NewObjectID(), models.Connection{Type: constants.DatabaseTypeSpreadsheet, Host: "old", Database: "fixture"}, models.DefaultChatSettings())
	chat.SelectedCollections = "orders,items,shops,refunds"
	chat.SelectionRevision = 7
	chat.ObjectState = &models.State{Revision: 9}
	if err := utils.EncryptConnection(&chat.Connection); err != nil {
		t.Fatal(err)
	}
	repo := &legacyUpdateRepo{current: *chat}
	return &chatService{chatRepo: repo, sourceAccess: NewUnconfiguredSourceAccessService(repo), dbManager: &dbmanager.Manager{}}, repo
}
func legacyUpdateRequest(selected *string) *dtos.UpdateChatRequest {
	name := " after "
	return &dtos.UpdateChatRequest{DisplayName: &name, SelectedCollections: selected, Connection: &dtos.CreateConnectionRequest{
		Type: constants.DatabaseTypeSpreadsheet, Host: "new", Database: "fixture",
	}}
}
func TestLegacyUpdateConnectionAndSelectionAtomic(t *testing.T) {
	explicit := "sales,items"
	for _, enabled := range []bool{false, true} {
		for _, selected := range []*string{nil, &explicit} {
			t.Run(fmt.Sprintf("flag=%t/explicit=%t", enabled, selected != nil), func(t *testing.T) {
				s, r := newLegacyUpdateService(t, enabled)
				response, status, err := s.Update(r.current.UserID.Hex(), r.current.ID.Hex(), legacyUpdateRequest(selected))
				if err != nil || status != http.StatusOK {
					t.Fatalf("Update=%d %v stored selection=%q revision=%d", status, err, r.current.SelectedCollections, r.current.SelectionRevision)
				}
				want := ""
				if selected != nil {
					want = *selected
				}
				if response.Connection.Host != "new" || response.SelectedCollections != want || response.SelectionRevision != 8 ||
					r.current.ConnectionRevision != 1 || r.current.ObjectState != nil || r.current.DisplayName != "after" {
					t.Fatalf("connection and selection disagree: response=%+v stored=%+v", response, r.current)
				}
				if r.selectionPatches != 0 || r.atomicWrites != 1 {
					t.Fatalf("split selection write: patches=%d atomic=%d", r.selectionPatches, r.atomicWrites)
				}
			})
		}
	}
}
func TestLegacyUpdateCASFailureDoesNotExpandSelection(t *testing.T) {
	explicit := "ALL"
	for _, enabled := range []bool{false, true} {
		for _, selected := range []*string{nil, &explicit} {
			for _, concurrent := range []bool{false, true} {
				t.Run(fmt.Sprintf("flag=%t/explicit=%t/concurrent=%t", enabled, selected != nil, concurrent), func(t *testing.T) {
					s, r := newLegacyUpdateService(t, enabled)
					r.failCAS = !concurrent
					r.concurrentSelection = concurrent
					response, status, err := s.Update(r.current.UserID.Hex(), r.current.ID.Hex(), legacyUpdateRequest(selected))
					if response != nil || status != http.StatusConflict || err == nil {
						t.Fatalf("lost CAS accepted: %d %v", status, err)
					}
					connection := r.current.Connection.Clone()
					utils.DecryptConnection(&connection)
					want, revision := "orders,items,shops,refunds", int64(7)
					if concurrent {
						want, revision = "concurrent_only", 8
					}
					if connection.Host != "old" || r.current.SelectedCollections != want || r.current.SelectionRevision != revision ||
						r.current.ConnectionRevision != 0 || r.current.DisplayName != "" || r.selectionPatches != 0 {
						t.Fatalf("failed update changed selection/connection/metadata: %+v", r.current)
					}
				})
			}
		}
	}
}
func TestLegacyUpdateHonorsExpectedSelectionRevision(t *testing.T) {
	s, r := newLegacyUpdateService(t, true)
	stale := int64(6)
	req := legacyUpdateRequest(nil)
	req.SelectionRevision = &stale
	_, status, err := s.Update(r.current.UserID.Hex(), r.current.ID.Hex(), req)
	if status != http.StatusConflict || !errors.Is(err, ErrSelectionConflict) || r.atomicWrites != 0 || r.selectionPatches != 0 {
		t.Fatalf("stale selection request changed chat: status=%d err=%v", status, err)
	}
}
func TestLegacyUpdateUnresolvedERPDoesNotChangeSelection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			s, r := newLegacyUpdateService(t, enabled)
			r.current.ERPAssociated = true
			all := "ALL"
			_, status, err := s.Update(r.current.UserID.Hex(), r.current.ID.Hex(), legacyUpdateRequest(&all))
			if status != http.StatusConflict || !errors.Is(err, ErrSourceUnresolved) {
				t.Fatalf("source gate bypassed: %d %v", status, err)
			}
			if r.current.SelectedCollections != "orders,items,shops,refunds" || r.current.SelectionRevision != 7 || r.selectionPatches != 0 {
				t.Fatal("rejected source binding overwrote selection")
			}
		})
	}
}
