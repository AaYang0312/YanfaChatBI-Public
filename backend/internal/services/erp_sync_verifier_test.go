package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"yanfachatbi/config"
	"yanfachatbi/internal/models"
)

type erpSyncPGFixture struct {
	db                          *sql.DB
	runtime                     config.ERPSourceRuntime
	writerDSN, readerDSN        string
	mapping                     map[string]string
	after                       string
	createdTable, createdSchema bool
}

func newERPSyncPGFixture(t *testing.T) *erpSyncPGFixture {
	t.Helper()
	dsn := os.Getenv("ERP_SYNC_TEST_PG_URL")
	if dsn == "" {
		t.Skip("UNVERIFIED: ERP_SYNC_TEST_PG_URL must name isolated bi_agent with fixture DDL/role privileges")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Path != "/bi_agent" {
		t.Fatal("isolated bi_agent URL required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("isolated PG open failed")
	}
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(0)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var control string
	if db.QueryRowContext(ctx, "SELECT current_user").Scan(&control) != nil {
		db.Close()
		t.Fatal("isolated PG unavailable")
	}
	suffix := uuid.New().String()[:8]
	reader, writer := "t39_reader_"+suffix, "t39_writer_"+suffix
	password := uuid.NewString()
	f := &erpSyncPGFixture{db: db, mapping: map[string]string{}, after: "test.after." + suffix}
	var private string
	createdRoles := []string{}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if len(f.mapping) == 7 {
			_, _ = db.ExecContext(cleanup, `DELETE FROM bi.sync_state WHERE source=$1 OR source=$2`, f.mapping["1"], f.after)
		}
		if f.createdTable {
			_, _ = db.ExecContext(cleanup, "DROP TABLE IF EXISTS bi.sync_state")
		}
		if f.createdSchema {
			_, _ = db.ExecContext(cleanup, "DROP SCHEMA IF EXISTS bi")
		}
		for _, role := range createdRoles {
			_, _ = db.ExecContext(cleanup, "DROP OWNED BY "+pq.QuoteIdentifier(role))
			_, _ = db.ExecContext(cleanup, "DROP ROLE "+pq.QuoteIdentifier(role))
		}
		db.Close()
		if private != "" {
			os.RemoveAll(private)
		}
	})
	for _, role := range []string{reader, writer} {
		if _, err := db.ExecContext(ctx, "CREATE ROLE "+pq.QuoteIdentifier(role)+" LOGIN PASSWORD "+pq.QuoteLiteral(password)); err != nil {
			t.Fatal("manager fixture requires isolated CREATE ROLE privilege")
		}
		createdRoles = append(createdRoles, role)
		if _, err := db.ExecContext(ctx, "GRANT CONNECT ON DATABASE bi_agent TO "+pq.QuoteIdentifier(role)); err != nil {
			t.Fatal("isolated role database CONNECT grant failed")
		}
	}
	readerURL, writerURL := *parsed, *parsed
	readerURL.User = url.UserPassword(reader, password)
	writerURL.User = url.UserPassword(writer, password)
	f.readerDSN = readerURL.String()
	f.writerDSN = writerURL.String()
	var table, schema sql.NullString
	if db.QueryRowContext(ctx, "SELECT to_regclass('bi.sync_state')::text,to_regnamespace('bi')::text").Scan(&table, &schema) != nil {
		t.Fatal("fixture object observation failed")
	}
	f.createdTable = !table.Valid
	f.createdSchema = !schema.Valid
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS bi; CREATE TABLE IF NOT EXISTS bi.sync_state(source text NOT NULL,entity text NOT NULL,shop_id text NOT NULL,watermark timestamptz,last_error_code text,last_success_at timestamptz,PRIMARY KEY(source,entity,shop_id))`); err != nil {
		t.Fatal("fixture sync_state setup failed")
	}
	if _, err := db.ExecContext(ctx, "GRANT USAGE ON SCHEMA bi TO "+pq.QuoteIdentifier(writer)+"; GRANT SELECT,UPDATE ON bi.sync_state TO "+pq.QuoteIdentifier(writer)); err != nil {
		t.Fatal("isolated writer grant failed")
	}
	for _, shop := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		f.mapping[shop] = "test.orders." + suffix
	}
	before := time.Now().Add(-time.Hour).Truncate(time.Second).Add(123456 * time.Microsecond)
	for shop, order := range f.mapping {
		for _, pair := range [][2]string{{order, "orders"}, {f.after, "aftersales_occurrence"}} {
			if _, err := db.ExecContext(ctx, `INSERT INTO bi.sync_state(source,entity,shop_id,watermark,last_error_code,last_success_at) VALUES($1,$2,$3,$4,NULL,$4)`, pair[0], pair[1], shop, before); err != nil {
				t.Fatal("synthetic watermark setup failed")
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	private, err = os.MkdirTemp(home, ".t39-control-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(private, "control.json")
	raw, _ := json.Marshal(struct {
		DSN string `json:"dsn"`
	}{dsn})
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("private fixture reference failed")
	}
	f.runtime = config.ERPSourceRuntime{ControlReader: config.SecretFileReference{Reference: "isolated-control", Path: path, UID: uint32(os.Getuid()), Mode: 0600}, ReaderRoleByRef: map[string]string{"isolated-reader-ref": reader}, WriterRole: writer, ControlRole: control, ObservationApprovalRef: "isolated-observation", AftersaleSource: f.after}
	return f
}
func (f *erpSyncPGFixture) bind(job *models.DataSyncJob) {
	job.SourceMapping = f.mapping
	job.AftersaleSource = f.after
}
func (f *erpSyncPGFixture) advance(t *testing.T, job *models.DataSyncJob) {
	t.Helper()
	for _, s := range job.ExpectedStates {
		if _, err := f.db.Exec(`UPDATE bi.sync_state SET watermark=$4,last_error_code=NULL,last_success_at=$4 WHERE source=$1 AND entity=$2 AND shop_id=$3`, s.Source, s.Entity, s.ShopID, *job.Cutoff); err != nil {
			t.Fatal("synthetic watermark advance failed")
		}
	}
}

func TestERPSyncVerifierExactKeysLagAndSQLNull(t *testing.T) {
	pg := newERPSyncPGFixture(t)
	job := erpSyncProbeJob()
	pg.bind(job)
	_, source, lease := erpSyncProbeGate(t, job)
	source.PhysicalDatabase = "bi_agent"
	source.ReaderRoleRefs = []string{"isolated-reader-ref"}
	verifier := NewERPSyncVerifier(config.ERPSyncRuntime{Sources: map[string]config.ERPSourceRuntime{job.SourceID: pg.runtime}}, job.InstanceID)
	states, err := verifier.Preflight(context.Background(), source, job, lease)
	if err != nil {
		t.Fatal(err)
	}
	job.ExpectedStates = states
	cutoff := time.Now().Truncate(time.Second)
	job.Cutoff = &cutoff
	if result, err := verifier.Verify(context.Background(), source, job, lease); err == nil || result.Passed {
		t.Fatal("lagging real SQL watermarks succeeded")
	}
	pg.advance(t, job)
	if result, err := verifier.Verify(context.Background(), source, job, lease); err != nil || !result.Passed || len(result.Rows) != 14 {
		t.Fatalf("fixed 14-key SQL verification failed: %v", err)
	}
	first := job.ExpectedStates[0]
	if _, err := pg.db.Exec(`UPDATE bi.sync_state SET last_error_code='' WHERE source=$1 AND entity=$2 AND shop_id=$3`, first.Source, first.Entity, first.ShopID); err != nil {
		t.Fatal(err)
	}
	if result, err := verifier.Verify(context.Background(), source, job, lease); err == nil || result.Passed {
		t.Fatal("empty SQL string was treated as SQL NULL")
	}
	pg.advance(t, job)
	duplicate := append([]models.ERPSyncState(nil), job.ExpectedStates...)
	duplicate[13] = duplicate[0]
	job.ExpectedStates = duplicate
	if result, err := verifier.Verify(context.Background(), source, job, lease); err == nil || result.Passed {
		t.Fatal("duplicate key filled missing 14th entity")
	}
}

func TestERPSyncVerifierActualReaderFormsAndSurvivingPGSession(t *testing.T) {
	pg := newERPSyncPGFixture(t)
	job := erpSyncProbeJob()
	pg.bind(job)
	_, source, lease := erpSyncProbeGate(t, job)
	source.PhysicalDatabase = "bi_agent"
	source.ReaderRoleRefs = []string{"isolated-reader-ref"}
	verifier := NewERPSyncVerifier(config.ERPSyncRuntime{Sources: map[string]config.ERPSourceRuntime{job.SourceID: pg.runtime}}, job.InstanceID)
	instance := sha256.Sum256([]byte(job.InstanceID))
	startup := "yanfa:" + job.SourceID + ":" + hex.EncodeToString(instance[:10])
	parsed, _ := url.Parse(pg.readerDSN)
	query := parsed.Query()
	query.Set("application_name", startup)
	parsed.RawQuery = query.Encode()
	reader, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal("reader fixture open failed")
	}
	reader.SetMaxOpenConns(1)
	reader.SetMaxIdleConns(1)
	defer reader.Close()
	if reader.Ping() != nil {
		t.Fatal("reader fixture connection failed")
	}
	if err := verifier.Observe(context.Background(), source, job, lease); err != nil {
		t.Fatal("trusted current idle 63-byte pool was rejected")
	}
	conn, err := reader.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	sourceUUID := uuid.MustParse(job.SourceID)
	operation := uuid.MustParse(job.OperationID)
	hash := sha256.Sum256([]byte(startup))
	active := "s1:" + base64.RawURLEncoding.EncodeToString(sourceUUID[:]) + ":" + base64.RawURLEncoding.EncodeToString(hash[:9]) + ":" + base64.RawURLEncoding.EncodeToString(operation[:])
	var actual string
	if tx.QueryRow(`SELECT set_config('application_name',$1,true)`, active).Scan(&actual) != nil || len(actual) != 61 {
		t.Fatal("actual compact transaction identity was truncated")
	}
	if op, ok := syncReaderIdentity(actual, job.SourceID, job.InstanceID); !ok || op != job.OperationID {
		t.Fatal("trusted source/instance/operation did not decode")
	}
	if err := verifier.Observe(context.Background(), source, job, lease); err == nil {
		t.Fatal("active/idle-in-transaction reader survived EX admission")
	}
	tx.Rollback()
	conn.Close()
	if err := verifier.Observe(context.Background(), source, job, lease); err != nil {
		t.Fatal("actual reader transaction cleanup was not observed")
	}
	writer, err := sql.Open("postgres", pg.writerDSN)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxIdleConns(0)
	defer writer.Close()
	writerConn, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal("writer fixture connection failed")
	}
	var locked bool
	if writerConn.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock(7319041)`).Scan(&locked) != nil || !locked {
		t.Fatal("isolated writer lock fixture failed")
	}
	if err := verifier.Observe(context.Background(), source, job, lease); err == nil {
		t.Fatal("live PG writer session/lock was treated as cleaned")
	}
	writerConn.Close()
	if err := verifier.Observe(context.Background(), source, job, lease); err != nil {
		t.Fatal("actual writer session close was not observed")
	}
}
