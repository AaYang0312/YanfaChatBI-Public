package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/sourcegate"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

var errERPSyncObservation = errors.New("SYNC_OBSERVATION_UNAVAILABLE")
var errERPSyncVerification = errors.New("SYNC_VERIFICATION_FAILED")

// All SQL and object names are fixed. JSON keys are frozen server configuration,
// never SQL/identifiers supplied by HTTP, models or subprocess output.
const erpSyncVerifySQL = `WITH expected AS (
 SELECT source,entity,shop_id,before_watermark FROM jsonb_to_recordset($1::jsonb)
 AS e(source text,entity text,shop_id text,before_watermark timestamptz)
)
SELECT e.source,e.entity,e.shop_id,e.before_watermark,s.source IS NOT NULL,
 s.watermark,s.last_error_code,s.last_success_at,
 (s.source IS NOT NULL AND s.watermark IS NOT NULL AND s.watermark >= $2::timestamptz
 AND s.watermark >= e.before_watermark AND s.last_error_code IS NULL)
FROM expected e LEFT JOIN bi.sync_state s
 ON s.source=e.source AND s.entity=e.entity AND s.shop_id=e.shop_id
ORDER BY e.shop_id,e.entity,e.source`

const erpSyncPreflightSQL = `WITH expected AS (
 SELECT source,entity,shop_id FROM jsonb_to_recordset($1::jsonb)
 AS e(source text,entity text,shop_id text)
)
SELECT e.source,e.entity,e.shop_id,s.watermark
FROM expected e LEFT JOIN bi.sync_state s
 ON s.source=e.source AND s.entity=e.entity AND s.shop_id=e.shop_id
ORDER BY e.shop_id,e.entity,e.source`

type ERPSyncVerifier interface {
	Observe(context.Context, *models.ERPSource, *models.DataSyncJob, *sourcegate.Lease) error
	Preflight(context.Context, *models.ERPSource, *models.DataSyncJob, *sourcegate.Lease) ([]models.ERPSyncState, error)
	Verify(context.Context, *models.ERPSource, *models.DataSyncJob, *sourcegate.Lease) (*models.ERPSyncVerification, error)
}

type postgresERPSyncVerifier struct {
	runtime        config.ERPSyncRuntime
	instanceID     string
	controlTimeout time.Duration
}

func NewERPSyncVerifier(runtime config.ERPSyncRuntime, instanceID string) ERPSyncVerifier {
	return &postgresERPSyncVerifier{runtime: runtime, instanceID: instanceID, controlTimeout: syncDuration(runtime.ControlSeconds, 15)}
}

func syncDuration(value, fallback int) time.Duration {
	if value <= 0 || value > 86400 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}

func syncLease(source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease) error {
	if source == nil || lease == nil || lease.Mode() != sourcegate.Exclusive {
		return sourcegate.ErrIdentity
	}
	id := lease.Identity()
	if id.SourceID != source.SourceID || (job != nil && (job.Kind() == "" || id != jobSyncIdentity(job) || job.SourceID != source.SourceID)) {
		return sourcegate.ErrIdentity
	}
	return lease.Check(id)
}

func syncStateKey(source, entity, shop string) string {
	return source + "\x00" + entity + "\x00" + shop
}

func expectedSyncStates(job *models.DataSyncJob) ([]models.ERPSyncState, error) {
	if job == nil || len(job.ExpectedShops) != 7 || len(job.SourceMapping) != 7 || !safeSyncToken(job.AftersaleSource, 128) {
		return nil, errERPSyncVerification
	}
	states := make([]models.ERPSyncState, 0, 14)
	seen := make(map[string]bool, 7)
	for _, shop := range job.ExpectedShops {
		if !safeSyncDigits(shop) || seen[shop] || !safeSyncToken(job.SourceMapping[shop], 128) {
			return nil, errERPSyncVerification
		}
		seen[shop] = true
		states = append(states, models.ERPSyncState{Source: job.SourceMapping[shop], Entity: "orders", ShopID: shop}, models.ERPSyncState{Source: job.AftersaleSource, Entity: "aftersales_occurrence", ShopID: shop})
	}
	return states, nil
}

func validExpectedStates(job *models.DataSyncJob, states []models.ERPSyncState, cutoff time.Time) bool {
	wanted, err := expectedSyncStates(job)
	if err != nil || len(states) != 14 {
		return false
	}
	keys := make(map[string]bool, 14)
	for _, s := range wanted {
		keys[syncStateKey(s.Source, s.Entity, s.ShopID)] = true
	}
	floor := time.Date(1970, 1, 2, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	for _, s := range states {
		key := syncStateKey(s.Source, s.Entity, s.ShopID)
		if !keys[key] || !s.BeforeWatermark.After(floor) || s.BeforeWatermark.After(cutoff) {
			return false
		}
		delete(keys, key)
	}
	return len(keys) == 0
}

func (v *postgresERPSyncVerifier) connection(ctx context.Context, source *models.ERPSource) (*sql.DB, *sql.Conn, error) {
	runtime, ok := v.runtime.Sources[source.SourceID]
	if !ok || runtime.ObservationApprovalRef == "" || runtime.WriterRole == "" || runtime.ControlRole == "" || runtime.ControlRole == runtime.WriterRole || runtime.ControlReader.UID != source.DeploymentUID || len(source.ReaderRoleRefs) == 0 || len(runtime.ReaderRoleByRef) != len(source.ReaderRoleRefs) {
		return nil, nil, errERPSyncObservation
	}
	for _, ref := range source.ReaderRoleRefs {
		role := runtime.ReaderRoleByRef[ref]
		if role == "" || role == runtime.ControlRole || role == runtime.WriterRole {
			return nil, nil, errERPSyncObservation
		}
	}
	var secret struct {
		DSN string `json:"dsn"`
	}
	if err := readSyncPrivateJSON(runtime.ControlReader, &secret); err != nil || secret.DSN == "" {
		return nil, nil, errERPSyncObservation
	}
	db, err := sql.Open("postgres", secret.DSN)
	if err != nil {
		return nil, nil, errERPSyncObservation
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, errERPSyncObservation
	}
	var database, role string
	var observe bool
	err = conn.QueryRowContext(ctx, `SELECT current_database(),current_user,
 (pg_has_role(current_user,'pg_read_all_stats','MEMBER') OR (SELECT rolsuper FROM pg_roles WHERE rolname=current_user))`).Scan(&database, &role, &observe)
	if err != nil || database != source.PhysicalDatabase || role != runtime.ControlRole || !observe {
		conn.Close()
		db.Close()
		return nil, nil, errERPSyncObservation
	}
	return db, conn, nil
}

func syncRegisteredReaderRole(runtime config.ERPSourceRuntime, source *models.ERPSource, role string) bool {
	for _, ref := range source.ReaderRoleRefs {
		if runtime.ReaderRoleByRef[ref] == role {
			return true
		}
	}
	return false
}

// Session names are exact protocols, with canonical UUID decoding. No prefix
// matching or assumption that PostgreSQL preserves an overlong literal.
func syncReaderIdentity(name, source, instance string) (string, bool) {
	instanceHash := sha256.Sum256([]byte(instance))
	startup := "yanfa:" + source + ":" + hex.EncodeToString(instanceHash[:10])
	if name == startup {
		return "", true
	}
	parts := strings.Split(name, ":")
	if len(name) != 61 || len(parts) != 4 || parts[0] != "s1" {
		return "", false
	}
	sourceBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(sourceBytes) != 16 {
		return "", false
	}
	sourceID, err := uuid.FromBytes(sourceBytes)
	if err != nil || sourceID.String() != source || base64.RawURLEncoding.EncodeToString(sourceBytes) != parts[1] {
		return "", false
	}
	startupHash := sha256.Sum256([]byte(startup))
	if parts[2] != base64.RawURLEncoding.EncodeToString(startupHash[:9]) {
		return "", false
	}
	opBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(opBytes) != 16 || base64.RawURLEncoding.EncodeToString(opBytes) != parts[3] {
		return "", false
	}
	op, err := uuid.FromBytes(opBytes)
	if err != nil || op == uuid.Nil {
		return "", false
	}
	return op.String(), true
}

func (v *postgresERPSyncVerifier) Observe(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease) error {
	if err := syncLease(source, job, lease); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, v.controlTimeout)
	defer cancel()
	db, conn, err := v.connection(ctx, source)
	if err != nil {
		return err
	}
	defer db.Close()
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(7319041)`).Scan(&locked); err != nil || !locked {
		return errERPSyncObservation
	}
	// Unlock uses the same physical session and a separate cleanup budget even
	// after cancellation. Closing the control connection is the final guarantee.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), v.controlTimeout)
		defer cancel()
		var released bool
		_ = conn.QueryRowContext(cleanup, `SELECT pg_advisory_unlock(7319041)`).Scan(&released)
	}()
	runtime := v.runtime.Sources[source.SourceID]
	rows, err := conn.QueryContext(ctx, `SELECT usename,application_name,state,xact_start IS NOT NULL,query
 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend'`)
	if err != nil {
		return errERPSyncObservation
	}
	defer rows.Close()
	for rows.Next() {
		var role, name string
		var state, query sql.NullString
		var transaction bool
		if err := rows.Scan(&role, &name, &state, &transaction, &query); err != nil {
			return errERPSyncObservation
		}
		if role == runtime.WriterRole {
			return errERPSyncObservation
		}
		op, known := syncReaderIdentity(name, source.SourceID, v.instanceID)
		if syncRegisteredReaderRole(runtime, source, role) && known && op == "" && state.Valid && state.String == "idle" && !transaction {
			continue
		}
		// Active transactions must never survive EX admission, even if the
		// compact identity and SQL operation marker are both trustworthy.
		if syncRegisteredReaderRole(runtime, source, role) && known && op != "" {
			if !query.Valid || !strings.Contains(query.String, "/*yanfa_operation:"+op+"*/") {
				return errERPSyncObservation
			}
			return errERPSyncObservation
		}
		return errERPSyncObservation
	}
	if rows.Err() != nil {
		return errERPSyncObservation
	}
	return nil
}

func (v *postgresERPSyncVerifier) Preflight(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease) ([]models.ERPSyncState, error) {
	if err := syncLease(source, job, lease); err != nil {
		return nil, err
	}
	states, err := expectedSyncStates(job)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, v.controlTimeout)
	defer cancel()
	db, conn, err := v.connection(ctx, source)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, errERPSyncVerification
	}
	defer tx.Rollback()
	raw, _ := json.Marshal(states)
	rows, err := tx.QueryContext(ctx, erpSyncPreflightSQL, string(raw))
	if err != nil {
		return nil, errERPSyncVerification
	}
	actual := make([]models.ERPSyncState, 0, 14)
	for rows.Next() {
		var s models.ERPSyncState
		var watermark sql.NullTime
		if err := rows.Scan(&s.Source, &s.Entity, &s.ShopID, &watermark); err != nil {
			rows.Close()
			return nil, errERPSyncVerification
		}
		if !watermark.Valid {
			rows.Close()
			return nil, errERPSyncVerification
		}
		s.BeforeWatermark = watermark.Time
		actual = append(actual, s)
		if len(actual) > 14 {
			rows.Close()
			return nil, errERPSyncVerification
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !validExpectedStates(job, actual, time.Now()) {
		return nil, errERPSyncVerification
	}
	if tx.Commit() != nil {
		return nil, errERPSyncVerification
	}
	return actual, nil
}

func (v *postgresERPSyncVerifier) Verify(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease) (*models.ERPSyncVerification, error) {
	result := &models.ERPSyncVerification{Contract: "yanfa-source-sync/1:14-key", ObservedAt: time.Now().UTC()}
	if err := syncLease(source, job, lease); err != nil {
		return result, err
	}
	if job.Cutoff == nil || !validExpectedStates(job, job.ExpectedStates, *job.Cutoff) {
		return result, errERPSyncVerification
	}
	ctx, cancel := context.WithTimeout(ctx, v.controlTimeout)
	defer cancel()
	db, conn, err := v.connection(ctx, source)
	if err != nil {
		return result, err
	}
	defer db.Close()
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, errERPSyncVerification
	}
	defer tx.Rollback()
	raw, _ := json.Marshal(job.ExpectedStates)
	rows, err := tx.QueryContext(ctx, erpSyncVerifySQL, string(raw), *job.Cutoff)
	if err != nil {
		return result, errERPSyncVerification
	}
	for rows.Next() {
		var row models.ERPSyncVerificationRow
		var watermark, lastSuccess sql.NullTime
		var code sql.NullString
		var verified sql.NullBool
		if err := rows.Scan(&row.Source, &row.Entity, &row.ShopID, &row.BeforeWatermark, &row.Present, &watermark, &code, &lastSuccess, &verified); err != nil {
			rows.Close()
			return result, errERPSyncVerification
		}
		if watermark.Valid {
			value := watermark.Time
			row.Watermark = &value
		}
		if lastSuccess.Valid {
			value := lastSuccess.Time
			row.LastSuccessAt = &value
		}
		if code.Valid {
			safe := "ERROR_PRESENT"
			row.LastErrorCode = &safe
		}
		row.Verified = verified.Valid && verified.Bool
		result.Rows = append(result.Rows, row)
		if len(result.Rows) > 14 {
			rows.Close()
			return result, errERPSyncVerification
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || tx.Commit() != nil {
		return result, errERPSyncVerification
	}
	if err := validateSyncVerification(job, result); err != nil {
		return result, err
	}
	result.Passed = true
	return result, nil
}

func validateSyncVerification(job *models.DataSyncJob, result *models.ERPSyncVerification) error {
	if result == nil || job.Cutoff == nil || len(result.Rows) != 14 || !validExpectedStates(job, job.ExpectedStates, *job.Cutoff) {
		return errERPSyncVerification
	}
	keys := make(map[string]time.Time, 14)
	for _, s := range job.ExpectedStates {
		keys[syncStateKey(s.Source, s.Entity, s.ShopID)] = s.BeforeWatermark
	}
	for _, row := range result.Rows {
		key := syncStateKey(row.Source, row.Entity, row.ShopID)
		before, ok := keys[key]
		if !ok || !row.BeforeWatermark.Equal(before) || !row.Present || !row.Verified || row.LastErrorCode != nil || row.Watermark == nil || row.Watermark.Before(*job.Cutoff) || row.Watermark.Before(before) {
			return errERPSyncVerification
		}
		delete(keys, key)
	}
	if len(keys) != 0 {
		return errERPSyncVerification
	}
	return nil
}
