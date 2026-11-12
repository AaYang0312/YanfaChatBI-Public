package dbmanager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/redis"
)

// Full-package regression. The Windows extraction harness executes these same
// assertions and frozen production methods with fake surrounding runtime types;
// that harness does not execute real Redis, Sourcegate or Disconnect.
type invalidationRedis struct {
	redis.IRedisRepositories
	keys    map[string]bool
	deleted []string
	reads   int
	failKey string
	failure error
}

func (r *invalidationRedis) Del(key string, ctx context.Context) error {
	r.deleted = append(r.deleted, key)
	if key == r.failKey {
		return r.failure
	}
	delete(r.keys, key)
	return nil
}
func (r *invalidationRedis) Get(key string, ctx context.Context) (string, error) {
	r.reads++
	return "", errors.New("unexpected cache read")
}

type invalidationPolicy struct {
	SourceAccessPolicy
	failure error
	seen    []ConnectionConfig
}

func (p *invalidationPolicy) RequireRead(ctx context.Context, owner primitive.ObjectID, chat *models.Chat, connection models.Connection) error {
	if chat == nil || owner != chat.UserID {
		return errors.New("incorrect source identity")
	}
	p.seen = append(p.seen, ConnectionConfig{Type: connection.Type, SourceChat: chat})
	return p.failure
}

func invalidationFixture() (*Manager, *SchemaManager, ConnectionConfig, *invalidationRedis, *invalidationPolicy, []string, []string) {
	chat := &models.Chat{Base: models.Base{ID: primitive.NewObjectID()}, UserID: primitive.NewObjectID(), SourceID: "old-source", ConnectionRevision: 4, SourceBindingRevision: 7, ERPAssociated: true}
	config := ConnectionConfig{ChatID: chat.ID.Hex(), Type: "postgresql", SourceChat: chat}
	base := connectionSchemaKey(config.ChatID, config)
	old := []string{base, base + ":metadata-v2"}
	keep := []string{config.ChatID, config.ChatID + ":metadata-v2", config.ChatID + ":connection:5:old-source:7", config.ChatID + ":connection:4:old-source:8:metadata-v2", config.ChatID + ":connection:4:new-source:7:metadata-v2", "other-chat:connection:4:old-source:7"}
	repo := &invalidationRedis{keys: map[string]bool{}}
	policy := &invalidationPolicy{}
	manager := &Manager{connections: map[string]*Connection{}, sourceAccess: policy}
	sm := &SchemaManager{dbManager: manager, schemaCache: map[string]*SchemaInfo{}, storageService: &SchemaStorageService{redisRepo: repo}}
	manager.schemaManager = sm
	for _, key := range append(append([]string{}, old...), keep...) {
		sm.schemaCache[key] = &SchemaInfo{Tables: map[string]TableSchema{"old_table": {Name: "old_table"}}, Views: map[string]ViewSchema{"old_view": {Name: "old_view", Definition: "private_base"}}}
		repo.keys[schemaKeyPrefix+key] = true
	}
	return manager, sm, config, repo, policy, old, keep
}

func TestSchemaInvalidationWithoutActiveConnection(t *testing.T) {
	for _, state := range []string{"absent_after_restart", "disconnected", "connected"} {
		t.Run(state, func(t *testing.T) {
			manager, sm, config, repo, policy, old, keep := invalidationFixture()
			if state != "absent_after_restart" {
				status := StatusDisconnected
				if state == "connected" {
					status = StatusConnected
				}
				manager.connections[config.ChatID] = &Connection{ChatID: config.ChatID, UserID: config.SourceChat.UserID.Hex(), Config: config, Status: status}
			}
			err := manager.InvalidateConnection(context.Background(), config)
			if err != nil {
				t.Fatalf("err=%v redis_deletes=%d remaining_memory=%d", err, len(repo.deleted), len(sm.schemaCache))
			}
			wantDeleted := []string{schemaKeyPrefix + old[0], schemaKeyPrefix + old[1]}
			if !reflect.DeepEqual(repo.deleted, wantDeleted) {
				t.Fatalf("deleted=%v want=%v", repo.deleted, wantDeleted)
			}
			for _, key := range old {
				if sm.schemaCache[key] != nil || repo.keys[schemaKeyPrefix+key] {
					t.Errorf("old identity remains: %s", key)
				}
			}
			for _, key := range keep {
				if sm.schemaCache[key] == nil || !repo.keys[schemaKeyPrefix+key] {
					t.Errorf("unrelated identity removed: %s", key)
				}
			}
			if len(policy.seen) == 0 {
				t.Fatal("source authorization bypassed")
			}
			for _, seen := range policy.seen {
				if seen.SourceChat != config.SourceChat || seen.Type != config.Type {
					t.Fatal("authorized a different source snapshot")
				}
			}
			if manager.connections[config.ChatID] != nil {
				t.Fatal("existing disconnect flow not completed")
			}
		})
	}
}

func TestSchemaInvalidationErrorsRemainVisible(t *testing.T) {
	for _, version := range []int{0, 1} {
		t.Run([]string{"legacy_delete", "metadata_delete"}[version], func(t *testing.T) {
			manager, sm, config, repo, _, old, keep := invalidationFixture()
			failure := errors.New("synthetic redis delete failed")
			repo.failKey, repo.failure = schemaKeyPrefix+old[version], failure
			err := manager.InvalidateConnection(context.Background(), config)
			if !errors.Is(err, failure) {
				t.Fatalf("delete failure hidden: %v", err)
			}
			if len(repo.deleted) != 2 {
				t.Fatalf("other version not attempted: %v", repo.deleted)
			}
			for _, key := range old {
				if sm.schemaCache[key] != nil {
					t.Fatal("old in-memory identity remains")
				}
			}
			for _, key := range keep {
				if sm.schemaCache[key] == nil || !repo.keys[schemaKeyPrefix+key] {
					t.Fatal("unrelated identity removed")
				}
			}
			repo.failKey = ""
			if err := manager.InvalidateConnection(context.Background(), config); err != nil {
				t.Fatal("idempotent retry failed", err)
			}
			for _, key := range old {
				if repo.keys[schemaKeyPrefix+key] {
					t.Fatal("retry did not remove old key")
				}
			}
		})
	}
}

func TestSchemaInvalidationRequiresSourceAndIdentity(t *testing.T) {
	for _, condition := range []string{"denied", "cancelled", "missing_policy", "wrong_chat", "missing_snapshot", "missing_owner", "missing_storage"} {
		t.Run(condition, func(t *testing.T) {
			manager, sm, config, repo, policy, _, _ := invalidationFixture()
			ctx := context.Background()
			failure := errors.New("synthetic source denial")
			switch condition {
			case "denied":
				policy.failure = failure
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				failure = context.Canceled
			case "missing_policy":
				manager.sourceAccess = nil
			case "wrong_chat":
				config.ChatID = primitive.NewObjectID().Hex()
			case "missing_snapshot":
				config.SourceChat = nil
			case "missing_owner":
				config.SourceChat.UserID = primitive.NilObjectID
			case "missing_storage":
				sm.storageService = nil
			}
			count := len(sm.schemaCache)
			err := manager.InvalidateConnection(ctx, config)
			if err == nil {
				t.Fatal("unavailable authority/storage silently accepted")
			}
			if (condition == "denied" || condition == "cancelled") && !errors.Is(err, failure) {
				t.Fatal("source/context error hidden", err)
			}
			if len(repo.deleted) != 0 || len(sm.schemaCache) != count {
				t.Fatalf("failed authorization/storage mutated cache: deletes=%v memory=%d", repo.deleted, len(sm.schemaCache))
			}
		})
	}
}

func TestSchemaInvalidationDoesNotRelaxGetters(t *testing.T) {
	for _, state := range []string{"absent_after_restart", "disconnected", "unknown_type", "wrong_owner"} {
		t.Run(state, func(t *testing.T) {
			manager, sm, config, repo, _, _, _ := invalidationFixture()
			if state != "absent_after_restart" {
				status := StatusConnected
				if state == "disconnected" {
					status = StatusDisconnected
				}
				conn := &Connection{ChatID: config.ChatID, UserID: config.SourceChat.UserID.Hex(), Config: config, Status: status}
				if state == "unknown_type" {
					conn.Config.Type = ""
				}
				if state == "wrong_owner" {
					conn.UserID = primitive.NewObjectID().Hex()
				}
				manager.connections[config.ChatID] = conn
			}
			ctx := context.Background()
			if sm.IsSchemaReady(ctx, config.ChatID) {
				t.Fatal("old mixed cache became ready")
			}
			if got, err := sm.GetStoredSchemaInfo(ctx, config.ChatID); err == nil || got != nil {
				t.Fatal("getter returned untrusted schema")
			}
			if got, err := sm.GetStoredSchemaStorage(ctx, config.ChatID); err == nil || got != nil {
				t.Fatal("getter returned untrusted samples")
			}
			if repo.reads != 0 {
				t.Fatalf("getter read legacy storage %d times", repo.reads)
			}
		})
	}
}

// Existing callers, including the real Sourcegate integration test, retain the
// two-argument API. A known disconnected connection is enough for invalidation;
// no snapshot means no authority to guess which identity should be deleted.
func TestSchemaInvalidationLegacyAPI(t *testing.T) {
	for _, state := range []string{"connected", "disconnected", "legacy_unbound", "denied", "wrong_owner", "absent"} {
		t.Run(state, func(t *testing.T) {
			manager, sm, config, repo, policy, old, keep := invalidationFixture()
			conn := &Connection{ChatID: config.ChatID, UserID: config.SourceChat.UserID.Hex(), Config: config, Status: StatusConnected}
			wantErr := state == "denied" || state == "wrong_owner" || state == "absent"
			if state == "disconnected" {
				conn.Status = StatusDisconnected
			}
			if state == "legacy_unbound" {
				conn.Config.SourceChat = nil
				old, keep = []string{config.ChatID, config.ChatID + ":metadata-v2"}, append(append([]string{}, old...), keep[2:]...)
			}
			if state == "wrong_owner" {
				conn.UserID = primitive.NewObjectID().Hex()
			}
			denial := errors.New("synthetic source denial")
			if state == "denied" {
				policy.failure = denial
			}
			if state != "absent" {
				manager.connections[config.ChatID] = conn
			}
			count := len(sm.schemaCache)
			err := sm.InvalidateSchema(context.Background(), config.ChatID)
			if (err != nil) != wantErr {
				t.Fatalf("err=%v wantErr=%v", err, wantErr)
			}
			if wantErr {
				if len(repo.deleted) != 0 || len(sm.schemaCache) != count {
					t.Fatal("unauthorized legacy API mutated cache")
				}
				if state == "denied" && !errors.Is(err, denial) {
					t.Fatal("source denial hidden", err)
				}
				return
			}
			if len(policy.seen) == 0 {
				t.Fatal("legacy API bypassed source authorization")
			}
			if !reflect.DeepEqual(repo.deleted, []string{schemaKeyPrefix + old[0], schemaKeyPrefix + old[1]}) {
				t.Fatalf("wrong invalidated keys: %v", repo.deleted)
			}
			for _, key := range old {
				if sm.schemaCache[key] != nil || repo.keys[schemaKeyPrefix+key] {
					t.Fatalf("old identity remains: %s", key)
				}
			}
			for _, key := range keep {
				if sm.schemaCache[key] == nil || !repo.keys[schemaKeyPrefix+key] {
					t.Fatalf("unrelated identity removed: %s", key)
				}
			}
		})
	}
}
