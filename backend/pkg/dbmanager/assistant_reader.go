package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"yanfachatbi/pkg/sourcegate"
	"time"

	// The existing "postgres" driver (lib/pq) implements driver.ConnBeginTx
	// (conn_go18.go), so sql.TxOptions{ReadOnly, Isolation} reach PostgreSQL
	// as BEGIN ... ISOLATION LEVEL REPEATABLE READ READ ONLY. The named import
	// below also registers the driver via its package init.
	"github.com/lib/pq"
)

// Assistant read-only boundary (T05).
//
// The assistant source path never shares a connection pool with application
// writes: AssistantReadonlyReader owns one dedicated PostgreSQL connection and
// runs every statement inside
// sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}
// transactions. PostgreSQL then rejects any write with SQLSTATE 25006
// (read_only_sql_transaction); RunWriteProbe asserts exactly that.
//
// The reader never issues CREATE EXTENSION and never performs DDL against a
// customer database. Every statement it executes is recorded with its SQL text
// and its bound parameter values kept apart, so tests can prove that a hostile
// value such as "'; drop table shops; --" stayed one bound parameter and never
// became SQL text.

// AssistantReadonlyTxOptions is the only transaction shape the assistant
// source path may use.
func AssistantReadonlyTxOptions() sql.TxOptions {
	return sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}
}

// AssistantExecutedStatement records one statement the reader executed. Params
// are the bound values in order; they are recorded separately from SQL so a
// test can assert a value never leaked into SQL text.
type AssistantExecutedStatement struct {
	SQL    string
	Params []string
}

// AssistantSQLState extracts the five-character SQLSTATE from a PostgreSQL
// driver error (*pq.Error from lib/pq). ok is false for errors that carry no
// SQLSTATE.
func AssistantSQLState(err error) (string, bool) {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code), true
	}
	return "", false
}

// assistantProbeTable is the temporary table the write probe attempts. The
// name is fixed so the rejection is attributable in server logs.
const assistantProbeTable = "assistant_readonly_probe"

// AssistantReadonlyReader executes assistant source statements on one
// dedicated read-only PostgreSQL connection.
type AssistantReadonlyReader struct {
	db         *sql.DB
	statements []AssistantExecutedStatement
	policy     SourceAccessPolicy
	config     ConnectionConfig
	connection *Connection
}

// NewAssistantReadonlyReader opens a dedicated connection pool for the DSN.
// The pool holds exactly one connection: the reader is single-tenant by
// construction, and InUse dropping to zero proves a released connection is
// back to idle.
func NewAssistantReadonlyReader(dsn string) (*AssistantReadonlyReader, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open assistant readonly connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &AssistantReadonlyReader{db: db}, nil
}

// NewAssistantReadonlyReaderFromDB wraps an already-opened database/sql pool.
// Tests inject a fake driver through it; production callers use
// NewAssistantReadonlyReader.
func NewAssistantReadonlyReaderFromDB(db *sql.DB) *AssistantReadonlyReader {
	return &AssistantReadonlyReader{db: db}
}

// NewSourceAssistantReadonlyReader binds the dedicated reader to an admitted
// server Chat. The independent DSN constructors cannot consume an ERP context.
func NewSourceAssistantReadonlyReader(ctx context.Context, config ConnectionConfig, policy SourceAccessPolicy) (*AssistantReadonlyReader, error) {
	if policy == nil || config.SourceChat == nil || !config.SourceChat.ERPAssociated || config.SourceChat.SourceID == "" || config.ApplicationName == "" || len(config.ApplicationName) > 63 {
		return nil, sourcegate.ErrMissing
	}
	chat, connection := config.sourceSnapshot()
	if err := policy.RequireRead(ctx, chat.UserID, chat, connection); err != nil {
		return nil, err
	}
	bound, release, err := policy.AcquireSource(ctx, chat, connection)
	if err != nil {
		return nil, err
	}
	defer release()
	config.operationContext = bound
	physical, err := (&PostgresDriver{}).Connect(config)
	if err != nil {
		return nil, err
	}
	db, err := physical.DB.DB()
	if err != nil {
		(&PostgresDriver{}).Disconnect(physical)
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	config.operationContext = nil
	return &AssistantReadonlyReader{db: db, policy: policy, config: config, connection: physical}, nil
}

func (r *AssistantReadonlyReader) sourceContext(ctx context.Context) (context.Context, func(), error) {
	if r.policy == nil {
		if sourcegate.HasLease(ctx) {
			return nil, nil, sourcegate.ErrMissing
		}
		return ctx, func() {}, nil
	}
	chat, connection := r.config.sourceSnapshot()
	if err := r.policy.RequireRead(ctx, chat.UserID, chat, connection); err != nil {
		return nil, nil, err
	}
	return r.policy.AcquireSource(ctx, chat, connection)
}

// Ping verifies the DSN answers within a bounded timeout.
func (r *AssistantReadonlyReader) Ping(ctx context.Context) error {
	ctx, release, err := r.sourceContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return r.db.PingContext(ctx)
}

// Close closes the dedicated connection.
func (r *AssistantReadonlyReader) Close() error {
	if r.connection != nil {
		return (&PostgresDriver{}).Disconnect(r.connection)
	}
	return r.db.Close()
}

// Stats exposes the pool counters so tests can assert the connection returned
// to idle.
func (r *AssistantReadonlyReader) Stats() sql.DBStats { return r.db.Stats() }

// Statements returns a copy of the executed-statement log.
func (r *AssistantReadonlyReader) Statements() []AssistantExecutedStatement {
	out := make([]AssistantExecutedStatement, len(r.statements))
	copy(out, r.statements)
	return out
}

// record appends one executed statement to the log.
func (r *AssistantReadonlyReader) record(stmt AssistantExecutedStatement) {
	r.statements = append(r.statements, stmt)
}

// WithReadOnlyTx runs fn inside one read-only repeatable-read transaction on
// the dedicated connection. The transaction is committed, rolled back, or
// abandoned according to the context, and the connection is always released —
// including when fn cancels the context. T19 source reads are built on this
// primitive.
func (r *AssistantReadonlyReader) WithReadOnlyTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	ctx, release, err := r.sourceContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire assistant readonly connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck // releases the connection on every path

	opts := AssistantReadonlyTxOptions()
	tx, err := conn.BeginTx(ctx, &opts)
	if err != nil {
		return fmt.Errorf("begin assistant readonly transaction: %w", err)
	}
	if r.policy != nil {
		name, err := postgresOperationName(ctx, r.config)
		if err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, "SELECT set_config('application_name', $1, true)", name); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if ctx.Err() != nil {
		// The context died during fn: committing is meaningless and would
		// surface as a confusing driver error. Roll back and report the
		// cancellation itself.
		_ = tx.Rollback()
		return ctx.Err()
	}
	return tx.Commit()
}

// AssistantWriteProbeResult is the outcome of the canonical read-only
// verification transaction.
type AssistantWriteProbeResult struct {
	// Echo is the value the bound SELECT returned. It must equal the input.
	Echo string
	// WriteSQLState is the SQLSTATE PostgreSQL returned for the rejected
	// CREATE TEMP TABLE: "25006" when the boundary holds.
	WriteSQLState string
}

// errWriteRejected signals WithReadOnlyTx to roll back after the expected
// write rejection: PostgreSQL aborts the transaction on SQLSTATE 25006, so a
// commit would fail spuriously. RunWriteProbe maps it back to success.
var errWriteRejected = errors.New("assistant write probe: expected write rejection recorded")

// RunWriteProbe executes the T05 verification sequence inside one read-only
// transaction: a bound SELECT $1::text whose value must come back unchanged as
// data, then a CREATE TEMP TABLE attempt that PostgreSQL must reject with
// SQLSTATE 25006. The probe never issues CREATE EXTENSION.
func (r *AssistantReadonlyReader) RunWriteProbe(ctx context.Context, boundValue string) (AssistantWriteProbeResult, error) {
	var result AssistantWriteProbeResult
	err := r.WithReadOnlyTx(ctx, func(tx *sql.Tx) error {
		const selectSQL = "SELECT $1::text"
		r.record(AssistantExecutedStatement{SQL: selectSQL, Params: []string{boundValue}})
		if err := tx.QueryRowContext(ctx, selectSQL, boundValue).Scan(&result.Echo); err != nil {
			return fmt.Errorf("bound select failed: %w", err)
		}

		createSQL := "CREATE TEMP TABLE " + assistantProbeTable + "(id int)"
		r.record(AssistantExecutedStatement{SQL: createSQL})
		_, writeErr := tx.ExecContext(ctx, createSQL)
		if writeErr == nil {
			return errors.New("CREATE TEMP TABLE succeeded inside a read-only transaction; the assistant read-only boundary is not enforced")
		}
		state, ok := AssistantSQLState(writeErr)
		if !ok {
			return fmt.Errorf("write probe failed without a SQLSTATE: %w", writeErr)
		}
		result.WriteSQLState = state
		return errWriteRejected
	})
	if err != nil && !errors.Is(err, errWriteRejected) {
		return AssistantWriteProbeResult{}, err
	}
	return result, nil
}

// RunCancelProbe proves that cancelling the context terminates a running
// statement and releases the dedicated connection. It runs SELECT pg_sleep($1)
// with the given sleep seconds inside a read-only transaction, cancels the
// context after grace, and expects the statement to die with the context.
// pg_sleep is a read-side delay: it writes nothing and needs no privilege
// beyond connecting.
func (r *AssistantReadonlyReader) RunCancelProbe(ctx context.Context, sleepSeconds string, grace time.Duration) error {
	probeCtx, cancel := context.WithTimeout(ctx, grace)
	defer cancel()
	before := len(r.statements)
	err := r.WithReadOnlyTx(probeCtx, func(tx *sql.Tx) error {
		const sleepSQL = "SELECT pg_sleep($1)"
		r.record(AssistantExecutedStatement{SQL: sleepSQL, Params: []string{sleepSeconds}})
		var discard any
		scanErr := tx.QueryRowContext(probeCtx, sleepSQL, sleepSeconds).Scan(&discard)
		if scanErr == nil {
			return errors.New("pg_sleep completed before the cancellation grace elapsed; the probe proves nothing")
		}
		if probeCtx.Err() == nil {
			return fmt.Errorf("statement failed for a reason other than cancellation: %w", scanErr)
		}
		return nil
	})
	// The statement must actually have run: a BeginTx that timed out on a cold
	// connection is an environment failure, not a successful cancellation.
	attempted := false
	for _, stmt := range r.statements[before:] {
		if stmt.SQL == "SELECT pg_sleep($1)" {
			attempted = true
		}
	}
	if !attempted {
		return fmt.Errorf("cancel probe never reached the statement: %w", err)
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return err
	}
	stats := r.db.Stats()
	if stats.InUse != 0 {
		return fmt.Errorf("connection still in use after cancellation: InUse=%d Open=%d", stats.InUse, stats.OpenConnections)
	}
	return nil
}
