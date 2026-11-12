package dbmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/sourcegate"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The consumer is PostgreSQL itself: a long instance name must not truncate the
// source or operation identity actually visible inside an executing transaction.
func TestSourceGatePostgresOperationIdentity(t *testing.T) {
	raw := os.Getenv("SOURCE_ACCESS_TEST_PG_URL")
	if raw == "" {
		t.Skip("UNVERIFIED: SOURCE_ACCESS_TEST_PG_URL is required for the real PostgreSQL identity boundary")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil || strings.TrimPrefix(parsed.Path, "/") != "bi_agent" {
		t.Fatal("expected explicit isolated bi_agent PostgreSQL URL")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	sourceID, operationID := uuid.NewString(), uuid.NewString()
	target, err := gate.Provision(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	owner, chatID := primitive.NewObjectID(), primitive.NewObjectID()
	identity := sourcegate.Identity{SourceID: sourceID, OperationID: operationID, OwnerID: owner.Hex(), ChatID: chatID.Hex(), BindingRevision: 1, ConfigRevision: 1, ConnectionFingerprint: "isolated"}
	lease, err := gate.Acquire(context.Background(), target, sourcegate.Shared, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx := sourcegate.WithLease(context.Background(), lease)
	port, user, password := parsed.Port(), parsed.User.Username(), ""
	if port == "" {
		port = "5432"
	}
	if value, ok := parsed.User.Password(); ok {
		password = value
	}
	chat := models.NewChat(owner, models.Connection{}, models.DefaultChatSettings())
	chat.ID, chat.SourceID, chat.ERPAssociated, chat.SourceBindingConfirmed, chat.SourceBindingRevision = chatID, sourceID, true, true, 1
	cfg := ConnectionConfig{Type: "postgresql", Host: parsed.Hostname(), Port: &port, Username: &user, Password: &password, Database: "bi_agent", SourceChat: chat,
		ApplicationName: "yanfa:" + sourceID + ":" + strings.Repeat("long-instance-", 20), operationContext: ctx}
	driver := NewPostgresDriver()
	connection, err := driver.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Disconnect(connection)
	tx := driver.BeginTx(ctx, connection)
	if tx == nil {
		t.Fatal("admitted transaction did not start")
	}
	result, err := tx.ExecuteQuery(ctx, "SELECT current_setting('application_name') AS operation_name")
	if err != nil || result == nil || result.Error != nil {
		t.Fatalf("identity query failed: %v %+v", err, result)
	}
	var data struct {
		Results []struct {
			Name string `json:"operation_name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(result.StreamData, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Results) != 1 {
		t.Fatalf("identity query did not return its one session row: %s", result.StreamData)
	}
	actual := data.Results[0].Name
	parts := strings.Split(actual, ":")
	if len(actual) > 63 || len(parts) != 4 || parts[0] != "s1" {
		t.Fatalf("PostgreSQL truncated or lost operation identity: %q", actual)
	}
	sourceBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	operationBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatal(err)
	}
	source, err := uuid.FromBytes(sourceBytes)
	if err != nil || source.String() != sourceID {
		t.Fatalf("wrong source in real session identity: %v", err)
	}
	operation, err := uuid.FromBytes(operationBytes)
	if err != nil || operation.String() != operationID {
		t.Fatalf("wrong operation in real session identity: %v", err)
	}
	t.Logf("observed PostgreSQL application_name=%s source=%s operation=%s", actual, sourceID, operationID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if tx := driver.BeginTx(ctx, connection); tx != nil {
		tx.Rollback()
		t.Fatal("closed permit opened a new real transaction")
	}
	if _, err := postgresOperationQuery(ctx, cfg, "SELECT 1"); !errors.Is(err, sourcegate.ErrClosed) {
		t.Fatalf("closed permit error=%v", err)
	}
}
