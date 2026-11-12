package dbmanager

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// --- fake driver: verifies the reader's own contract without a database. ---
// It asserts BeginTx options and connection release only. It never stands in
// for the PostgreSQL acceptance: SQLSTATE 25006 is provable only against a
// real PostgreSQL through the DSN-gated test below.

type assistantFakeDriver struct{}

func (d assistantFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("assistant fake driver connects only through its connector")
}

type assistantFakeExec struct {
	query string
	args  []string
}

type assistantFakeConn struct {
	begins     []sql.TxOptions
	execs      []assistantFakeExec
	committed  int
	rolledBack int
	closed     bool
}

func (c *assistantFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &assistantFakeStmt{conn: c, query: query}, nil
}

func (c *assistantFakeConn) Close() error { c.closed = true; return nil }

func (c *assistantFakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("assistant fake driver requires BeginTx")
}

func (c *assistantFakeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.begins = append(c.begins, sql.TxOptions{
		ReadOnly:  opts.ReadOnly,
		Isolation: sql.IsolationLevel(opts.Isolation),
	})
	return &assistantFakeTx{conn: c}, nil
}

type assistantFakeStmt struct {
	conn  *assistantFakeConn
	query string
}

func (s *assistantFakeStmt) Close() error  { return nil }
func (s *assistantFakeStmt) NumInput() int { return -1 }

func (s *assistantFakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (s *assistantFakeStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	record := assistantFakeExec{query: s.query}
	for _, arg := range args {
		if value, ok := arg.Value.(string); ok {
			record.args = append(record.args, value)
		}
	}
	s.conn.execs = append(s.conn.execs, record)
	return driver.RowsAffected(0), nil
}

func (s *assistantFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("unexpected query in assistant fake driver")
}

type assistantFakeTx struct{ conn *assistantFakeConn }

func (t *assistantFakeTx) Commit() error   { t.conn.committed++; return nil }
func (t *assistantFakeTx) Rollback() error { t.conn.rolledBack++; return nil }

type assistantFakeConnector struct{ conn *assistantFakeConn }

func (c assistantFakeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c assistantFakeConnector) Driver() driver.Driver                        { return assistantFakeDriver{} }

// assistantFakeReader builds a reader over one fake connection.
func assistantFakeReader(t *testing.T) (*AssistantReadonlyReader, *assistantFakeConn) {
	t.Helper()
	conn := &assistantFakeConn{}
	db := sql.OpenDB(assistantFakeConnector{conn: conn})
	t.Cleanup(func() { _ = db.Close() })
	return NewAssistantReadonlyReaderFromDB(db), conn
}

func TestAssistantReadonlyReaderTxShape(t *testing.T) {
	ctx := context.Background()

	t.Run("every transaction is read-only repeatable-read on a released connection", func(t *testing.T) {
		reader, conn := assistantFakeReader(t)
		err := reader.WithReadOnlyTx(ctx, func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, "SELECT 1", "v")
			return execErr
		})
		if err != nil {
			t.Fatalf("WithReadOnlyTx: %v", err)
		}
		if len(conn.begins) != 1 {
			t.Fatalf("BeginTx calls = %d want 1", len(conn.begins))
		}
		if begin := conn.begins[0]; !begin.ReadOnly || begin.Isolation != sql.LevelRepeatableRead {
			t.Fatalf("BeginTx options = %+v, want ReadOnly repeatable-read", begin)
		}
		if conn.committed != 1 || conn.rolledBack != 0 {
			t.Fatalf("commit=%d rollback=%d, want commit=1 rollback=0", conn.committed, conn.rolledBack)
		}
		if stats := reader.Stats(); stats.InUse != 0 {
			t.Fatalf("connection still in use: %+v", stats)
		}
	})

	t.Run("an fn error rolls back and propagates", func(t *testing.T) {
		reader, conn := assistantFakeReader(t)
		sentinel := errors.New("read failed")
		err := reader.WithReadOnlyTx(ctx, func(tx *sql.Tx) error { return sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v want the fn error", err)
		}
		if conn.rolledBack != 1 || conn.committed != 0 {
			t.Fatalf("rollback=%d commit=%d, want rollback=1 commit=0", conn.rolledBack, conn.committed)
		}
	})

	t.Run("a context dying inside fn releases the connection instead of committing", func(t *testing.T) {
		reader, conn := assistantFakeReader(t)
		probeCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		err := reader.WithReadOnlyTx(probeCtx, func(tx *sql.Tx) error {
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v want context.Canceled", err)
		}
		if conn.committed != 0 {
			t.Fatalf("commit=%d after cancellation, want 0", conn.committed)
		}
		if stats := reader.Stats(); stats.InUse != 0 {
			t.Fatalf("connection still in use after cancellation: %+v", stats)
		}
	})

	t.Run("AssistantSQLState is absent for non-PG errors", func(t *testing.T) {
		if _, ok := AssistantSQLState(errors.New("plain failure")); ok {
			t.Fatal("a plain error must carry no SQLSTATE")
		}
	})
}

func TestAssistantReadonlyReaderAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("ASSISTANT_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("Q05 unverified: ASSISTANT_TEST_PG_DSN is not set; " +
			"the PostgreSQL read-only boundary (SQLSTATE 25006, cancellation, idle release) is not exercised")
	}
	ctx := context.Background()
	reader, err := NewAssistantReadonlyReader(dsn)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	if err := reader.Ping(ctx); err != nil {
		t.Fatalf("ping ASSISTANT_TEST_PG_DSN: %v", err)
	}

	probe, err := reader.RunWriteProbe(ctx, "'; drop table shops; --")
	if err != nil {
		t.Fatalf("write probe: %v", err)
	}
	if probe.Echo != "'; drop table shops; --" {
		t.Fatalf("bound value echo = %q, want the value returned unchanged as data", probe.Echo)
	}
	if probe.WriteSQLState != "25006" {
		t.Fatalf("write SQLSTATE = %q want 25006", probe.WriteSQLState)
	}
	for _, stmt := range reader.Statements() {
		if stmt.SQL == "SELECT $1::text" && (len(stmt.Params) != 1 || stmt.Params[0] != probe.Echo) {
			t.Fatalf("captured SELECT params = %#v, want exactly one bound value", stmt.Params)
		}
	}

	if err := reader.RunCancelProbe(ctx, "30", 3*time.Second); err != nil {
		t.Fatalf("cancel probe: %v", err)
	}
	for _, stmt := range reader.Statements() {
		if strings.Contains(strings.ToLower(stmt.SQL), "create extension") {
			t.Fatalf("statement log contains CREATE EXTENSION: %q", stmt.SQL)
		}
	}
	if stats := reader.Stats(); stats.InUse != 0 {
		t.Fatalf("connection still in use after the probes: %+v", stats)
	}
}
