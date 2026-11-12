//go:build linux

package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"yanfachatbi/config"
	"yanfachatbi/internal/erpsyncinstall"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/sourcegate"

	"golang.org/x/sys/unix"
)

const erpBridgeProtocol = "yanfa-erp-bridge/1"
const syncOutputLimit = 1 << 20
const syncLineLimit = 16 << 10
const syncEventLimit = 64

var errSyncRelease = errors.New("SYNC_RELEASE_UNAVAILABLE")
var errSyncProtocol = errors.New("RUNNER_PROTOCOL_INVALID")
var errSyncProcess = errors.New("RUNNER_PROCESS_UNCERTAIN")

type erpSyncLaunch struct {
	executable, directory string
	args, environment     []string
	executableDigest      string
}

type erpSyncRunResult struct {
	ExitConfirmed bool
	ExitCode      *int
	SafeCode      string
}

type syncStartFrame struct {
	Protocol        string                `json:"protocol"`
	Type            string                `json:"type"`
	JobID           string                `json:"job_id"`
	SourceID        string                `json:"source_id"`
	OperationID     string                `json:"operation_id"`
	InstanceID      string                `json:"instance_id"`
	Mode            string                `json:"mode"`
	LockFD          int                   `json:"lock_fd"`
	ComponentRef    string                `json:"component_ref"`
	ComponentDigest string                `json:"component_digest"`
	BridgeVersion   string                `json:"bridge_version"`
	ConfigRef       string                `json:"config_ref"`
	ConfigRevision  int64                 `json:"config_revision"`
	ScopeDigest     string                `json:"scope_digest"`
	ExpectedShopIDs []string              `json:"expected_shop_ids"`
	SourceMapping   map[string]string     `json:"source_mapping"`
	AftersaleSource string                `json:"aftersale_source"`
	ExpectedStates  []models.ERPSyncState `json:"expected_states"`
}

type ERPSyncRunner interface {
	Load(*models.ERPSource) error
	Ready(*models.ERPSource) (string, string, error)
	Prepare(context.Context, *models.ERPSource, *models.DataSyncJob) (*erpSyncLaunch, error)
	Run(context.Context, *models.ERPSource, *models.DataSyncJob, *sourcegate.Lease, *erpSyncLaunch, func(*models.DataSyncJob) error) erpSyncRunResult
	Recover(context.Context, *models.ERPSource, *models.DataSyncJob, *sourcegate.Lease) error
}

type processERPSyncRunner struct {
	runtime                                        config.ERPSyncRuntime
	uid                                            uint32
	startBudget, runBudget, termBudget, killBudget time.Duration
	readyMu                                        sync.Mutex
	ready                                          map[string]syncReleaseReadiness
}

type syncReleaseReadiness struct {
	revision                   int64
	reference, digest, version string
	err                        error
}

func NewERPSyncRunner(runtime config.ERPSyncRuntime, uid uint32) ERPSyncRunner {
	return &processERPSyncRunner{runtime: runtime, uid: uid, startBudget: syncDuration(runtime.StartSeconds, 10), runBudget: syncDuration(runtime.RunSeconds, 3600), termBudget: syncDuration(runtime.TermSeconds, 10), killBudget: syncDuration(runtime.KillSeconds, 5), ready: map[string]syncReleaseReadiness{}}
}

func safeSyncToken(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:/@+-", c)) {
			return false
		}
	}
	return true
}
func safeSyncDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func syncDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func syncHashFile(file *os.File) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Open each component with O_NOFOLLOW. Immutable installation files and their
// parents must be root-owned and never group/world writable. Private files use
// the explicitly approved UID/mode and are parsed as JSON, never shell source.
func openSyncFile(path string, uid uint32, mode uint32, immutable bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return nil, errSyncRelease
	}
	dir, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errSyncRelease
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(dir, part, flags, 0)
		unix.Close(dir)
		if openErr != nil {
			return nil, errSyncRelease
		}
		dir = next
		var st unix.Stat_t
		if unix.Fstat(dir, &st) != nil {
			unix.Close(dir)
			return nil, errSyncRelease
		}
		if i < len(parts)-1 {
			if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0022 != 0 || (st.Uid != 0 && (immutable || st.Uid != uid)) {
				unix.Close(dir)
				return nil, errSyncRelease
			}
		} else if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uid || st.Mode&0022 != 0 || (mode != 0 && st.Mode&0777 != mode) {
			unix.Close(dir)
			return nil, errSyncRelease
		}
	}
	return os.NewFile(uintptr(dir), "sync-approved-file"), nil
}

func readSyncJSONFile(path, digest string, target interface{}) error {
	if len(digest) != 64 {
		return errSyncRelease
	}
	file, err := openSyncFile(path, 0, 0, true)
	if err != nil {
		return errSyncRelease
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(raw) > 1<<20 || syncDigest(raw) != digest {
		return errSyncRelease
	}
	// Release receipts contain absolute paths as keys. Use the same strict
	// metadata decoder as the installer; bridge event limits are separate.
	if erpsyncinstall.DecodeJSON(raw, target) != nil {
		return errSyncRelease
	}
	return nil
}
func readSyncPrivateJSON(ref config.SecretFileReference, target interface{}) error {
	if ref.Reference == "" || ref.Mode != 0600 {
		return errSyncRelease
	}
	file, err := openSyncFile(ref.Path, ref.UID, ref.Mode, false)
	if err != nil {
		return errSyncRelease
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return errSyncRelease
	}
	if strictSyncJSON(raw, target) != nil {
		return errSyncRelease
	}
	return nil
}
func syncNamespace(kind string) (string, error) {
	value, err := os.Readlink("/proc/self/ns/" + kind)
	if err != nil {
		return "", errSyncProcess
	}
	return value, nil
}
func syncBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", errSyncProcess
	}
	id := strings.TrimSpace(string(raw))
	if !models.CanonicalSyncUUID(id) {
		return "", errSyncProcess
	}
	return id, nil
}

func (r *processERPSyncRunner) installation(source *models.ERPSource) (*config.ERPComponentManifest, *config.ERPInstallationReceipt, error) {
	rt, ok := r.runtime.Sources[source.SourceID]
	if !ok || rt.ManifestRef != source.ComponentManifestRef || rt.PrivateEnvironment.Reference != source.SecretInjectionRef || rt.G15ApprovalRef == "" || rt.SingleBackendApprovalRef == "" || rt.ObservationApprovalRef == "" || source.ApprovalRefs["G15-0"] == "" || source.ApprovalRefs["Q15-01"] == "" || source.ApprovalRefs["Q15-03"] == "" || source.ApprovalRefs["Q15-04"] == "" || source.DeploymentUID != r.uid || uint32(os.Geteuid()) != r.uid {
		return nil, nil, errSyncRelease
	}
	if rt.PrivateEnvironment.UID != r.uid || rt.ControlReader.UID != r.uid {
		return nil, nil, errSyncRelease
	}
	var manifest config.ERPComponentManifest
	var receipt config.ERPInstallationReceipt
	if readSyncJSONFile(rt.ManifestPath, rt.ManifestSHA256, &manifest) != nil || readSyncJSONFile(rt.InstallationPath, rt.InstallationSHA256, &receipt) != nil {
		return nil, nil, errSyncRelease
	}
	if manifest.ComponentOwnerRef == "" || manifest.RepositoryURI == "" || manifest.CommitOrRelease == "" || len(manifest.ArtifactSHA256) != 64 || len(manifest.LockSHA256) != 64 || manifest.BridgeProtocol != erpBridgeProtocol || !safeSyncToken(manifest.BridgeVersion, 64) || manifest.PythonVersion == "" || manifest.UVVersion == "" || manifest.InstallRecipe == "" || manifest.DeploymentUID != r.uid || manifest.ConfigRef != source.SecretInjectionRef || manifest.ApprovedBy == "" || receipt.ApprovedBy == "" {
		return nil, nil, errSyncRelease
	}
	if len(manifest.MaintenanceOperations) > 1 || len(manifest.MaintenanceOperations) == 1 && manifest.MaintenanceOperations[0] != "incremental" {
		return nil, nil, errSyncRelease
	}
	if _, err := time.Parse(time.RFC3339, manifest.ApprovedAt); err != nil {
		return nil, nil, errSyncRelease
	}
	if _, err := time.Parse(time.RFC3339, receipt.ApprovedAt); err != nil {
		return nil, nil, errSyncRelease
	}
	if receipt.ManifestSHA256 != rt.ManifestSHA256 || receipt.PythonVersion != manifest.PythonVersion || receipt.UVVersion != manifest.UVVersion || receipt.InstallRecipe != manifest.InstallRecipe || len(receipt.ExecutableSHA256) != 64 || len(receipt.InstalledFiles) == 0 || len(receipt.InstalledFiles) > 4096 {
		return nil, nil, errSyncRelease
	}
	mnt, err := syncNamespace("mnt")
	if err != nil {
		return nil, nil, err
	}
	pidns, err := syncNamespace("pid")
	if err != nil {
		return nil, nil, err
	}
	if mnt != rt.RuntimeMountNamespace || pidns != rt.RuntimePIDNamespace || mnt != receipt.RuntimeMountNamespace || pidns != receipt.RuntimePIDNamespace {
		return nil, nil, errSyncRelease
	}
	files := make(map[string]string, len(receipt.InstalledFiles)+3)
	for path, digest := range receipt.InstalledFiles {
		files[path] = digest
	}
	if digest, ok := files[manifest.ExecutableAbsolutePath]; ok && digest != receipt.ExecutableSHA256 {
		return nil, nil, errSyncRelease
	}
	files[manifest.ExecutableAbsolutePath] = receipt.ExecutableSHA256
	files[receipt.ArtifactPath] = manifest.ArtifactSHA256
	files[receipt.LockPath] = manifest.LockSHA256
	bridgeInstalled := false
	for path, digest := range files {
		if strings.HasSuffix(path, "/bi_agent/yanfa_bridge.py") {
			bridgeInstalled = true
		}
		file, err := openSyncFile(path, 0, 0, true)
		if err != nil {
			return nil, nil, errSyncRelease
		}
		actual, hashErr := syncHashFile(file)
		file.Close()
		if hashErr != nil || actual != digest {
			return nil, nil, errSyncRelease
		}
	}
	if !bridgeInstalled || !filepath.IsAbs(manifest.WorkingDirectory) || filepath.Clean(manifest.WorkingDirectory) != manifest.WorkingDirectory {
		return nil, nil, errSyncRelease
	}
	// Opening a sentinel inside the working directory validates its complete
	// trusted parent chain without following a directory symlink.
	if !strings.HasPrefix(receipt.LockPath, manifest.WorkingDirectory+"/") {
		return nil, nil, errSyncRelease
	}
	return &manifest, &receipt, nil
}

func (r *processERPSyncRunner) Load(source *models.ERPSource) error {
	manifest, _, err := r.installation(source)
	if err == nil {
		rt := r.runtime.Sources[source.SourceID]
		for _, ref := range []config.SecretFileReference{rt.PrivateEnvironment, rt.ControlReader} {
			if ref.Reference == "" || ref.Mode != 0600 {
				err = errSyncRelease
				break
			}
			file, openErr := openSyncFile(ref.Path, ref.UID, ref.Mode, false)
			if openErr != nil {
				err = errSyncRelease
				break
			}
			file.Close()
		}
	}
	entry := syncReleaseReadiness{revision: source.ConfigRevision, reference: source.ComponentManifestRef, err: err}
	if err == nil {
		entry.digest = r.runtime.Sources[source.SourceID].ManifestSHA256
		entry.version = manifest.BridgeVersion
	}
	r.readyMu.Lock()
	r.ready[source.SourceID] = entry
	r.readyMu.Unlock()
	return err
}

// Status only reads startup-validated metadata. Prepare revalidates immutable
// installation bytes immediately before admission; status never reads secrets.
func (r *processERPSyncRunner) Ready(source *models.ERPSource) (string, string, error) {
	r.readyMu.Lock()
	entry, ok := r.ready[source.SourceID]
	r.readyMu.Unlock()
	if !ok || entry.revision != source.ConfigRevision || entry.reference != source.ComponentManifestRef || entry.err != nil {
		return "", "", errSyncRelease
	}
	return entry.digest, entry.version, nil
}

func (r *processERPSyncRunner) Prepare(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob) (*erpSyncLaunch, error) {
	if ctx.Err() != nil {
		return nil, errSyncRelease
	}
	manifest, receipt, err := r.installation(source)
	if err != nil {
		return nil, err
	}
	if job.Kind() == models.ExternalMaintenanceOperation && (len(manifest.MaintenanceOperations) != 1 || manifest.MaintenanceOperations[0] != "incremental") {
		return nil, errSyncRelease
	}
	rt := r.runtime.Sources[source.SourceID]
	if job.ComponentDigest != rt.ManifestSHA256 || job.BridgeVersion != manifest.BridgeVersion || job.ConfigRevision != source.ConfigRevision {
		return nil, errSyncRelease
	}
	var values map[string]string
	if readSyncPrivateJSON(rt.PrivateEnvironment, &values) != nil {
		return nil, errSyncRelease
	}
	keys := []string{"BI_WRITER_DSN", "BI_SHOP_IDS", "KUAI_MAI_APP_KEY", "KUAI_MAI_APP_SECRET", "KUAI_MAI_ACCESS_TOKEN", "KUAI_MAI_REFRESH_TOKEN"}
	if len(values) != len(keys) {
		return nil, errSyncRelease
	}
	environment := []string{"PYTHONNOUSERSITE=1", "PYTHONSAFEPATH=1", "PYTHONDONTWRITEBYTECODE=1", "LANG=C.UTF-8", "TZ=Asia/Shanghai"}
	for _, key := range keys {
		value, ok := values[key]
		if !ok || value == "" || len(value) > 16384 || strings.ContainsRune(value, 0) {
			return nil, errSyncRelease
		}
		environment = append(environment, key+"="+value)
	}
	shops := strings.Split(values["BI_SHOP_IDS"], ",")
	sort.Strings(shops)
	if len(shops) != len(job.ExpectedShops) {
		return nil, errSyncRelease
	}
	for i := range shops {
		if shops[i] != job.ExpectedShops[i] {
			return nil, errSyncRelease
		}
	}
	return &erpSyncLaunch{executable: manifest.ExecutableAbsolutePath, directory: manifest.WorkingDirectory, args: []string{"-u", "-m", "bi_agent.yanfa_bridge", "incremental"}, environment: environment, executableDigest: receipt.ExecutableSHA256}, nil
}

// strictSyncJSON rejects duplicate keys as well as unknown fields. Decoder's
// usual last-key-wins behavior is unsafe for identity/control frames.
func strictSyncJSON(raw []byte, target interface{}) error {
	return strictSyncEventJSON(raw, target, "")
}

func strictSyncEventJSON(raw []byte, target interface{}, eventType string) error {
	if !utf8.Valid(raw) {
		return errSyncProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int, string) error
	value = func(depth int, parent string) error {
		if depth > 12 {
			return errSyncProtocol
		}
		token, err := decoder.Token()
		if err != nil {
			return errSyncProtocol
		}
		if token == nil {
			return errSyncProtocol
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return errSyncProtocol
					}
					name, ok := key.(string)
					if !ok || seen[name] || len(name) > 128 || len(seen) > 4096 {
						return errSyncProtocol
					}
					if eventType != "" && !syncEventFieldAllowed(eventType, parent, name) {
						return errSyncProtocol
					}
					seen[name] = true
					if err := value(depth+1, name); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return errSyncProtocol
				}
			case '[':
				count := 0
				for decoder.More() {
					count++
					if count > 4096 {
						return errSyncProtocol
					}
					if err := value(depth+1, parent); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim(']') {
					return errSyncProtocol
				}
			default:
				return errSyncProtocol
			}
		}
		return nil
	}
	if value(0, "") != nil {
		return errSyncProtocol
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errSyncProtocol
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return errSyncProtocol
	}
	return nil
}

// Event keys are case-sensitive on the wire. encoding/json's struct matching
// folds case, so check exact names during the existing token walk, before
// decoding any struct or comparing a canonical replay digest.
func syncEventFieldAllowed(eventType, parent, name string) bool {
	if parent == "expected_states" && eventType == "run_started" {
		switch name {
		case "source", "entity", "shop_id", "before_watermark":
			return true
		}
		return false
	}
	if parent != "" {
		return false
	}
	switch name {
	case "protocol", "job_id", "source_id", "operation_id", "seq", "type":
		return true
	}
	switch eventType {
	case "run_started":
		switch name {
		case "bridge_version", "component_ref", "mode", "cutoff", "config_revision", "scope_digest", "expected_shop_ids", "expected_states":
			return true
		}
	case "shop_finished":
		switch name {
		case "shop_id", "status", "order_source", "orders_count", "aftersales_occurrence_count", "related_orders_refetch_completed", "safe_error_code":
			return true
		}
	case "run_finished":
		switch name {
		case "cutoff", "expected_count", "processed_count", "succeeded_count", "failed_count", "shop_results_digest":
			return true
		}
	}
	return false
}

type syncEventHeader struct {
	Protocol    string `json:"protocol"`
	JobID       string `json:"job_id"`
	SourceID    string `json:"source_id"`
	OperationID string `json:"operation_id"`
	Seq         int64  `json:"seq"`
	Type        string `json:"type"`
}

type syncStartedEvent struct {
	syncEventHeader
	BridgeVersion   string                `json:"bridge_version"`
	ComponentRef    string                `json:"component_ref"`
	Mode            string                `json:"mode"`
	Cutoff          string                `json:"cutoff"`
	ConfigRevision  int64                 `json:"config_revision"`
	ScopeDigest     string                `json:"scope_digest"`
	ExpectedShopIDs []string              `json:"expected_shop_ids"`
	ExpectedStates  []models.ERPSyncState `json:"expected_states"`
}

type syncShopEvent struct {
	syncEventHeader
	models.ERPSyncShopResult
}
type syncFinishedEvent struct {
	syncEventHeader
	Cutoff            string `json:"cutoff"`
	ExpectedCount     int    `json:"expected_count"`
	ProcessedCount    int    `json:"processed_count"`
	SucceededCount    int    `json:"succeeded_count"`
	FailedCount       int    `json:"failed_count"`
	ShopResultsDigest string `json:"shop_results_digest"`
}

type syncProtocol struct {
	job        *models.DataSyncJob
	seen       map[int64]string
	shops      map[string]models.ERPSyncShopResult
	finished   bool
	cutoffText string
	lines      int
}

func newSyncProtocol(job *models.DataSyncJob) *syncProtocol {
	return &syncProtocol{job: job, seen: map[int64]string{}, shops: map[string]models.ERPSyncShopResult{}}
}

func syncShopDigest(results []models.ERPSyncShopResult) string {
	ordered := append([]models.ERPSyncShopResult(nil), results...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ShopID < ordered[j].ShopID })
	raw, _ := json.Marshal(ordered)
	return syncDigest(raw)
}
func syncSafeShopCode(code string) string {
	switch code {
	case "SHOP_FAILED", "ERP_REJECTED", "WATERMARK_INVALID", "SOURCE_MAPPING_MISMATCH", "WRITER_BUSY", "RUNNER_FAILED":
		return code
	default:
		return "RUNNER_FAILED"
	}
}

func (p *syncProtocol) accept(raw []byte) (*models.DataSyncJob, error) {
	p.lines++
	if p.lines > syncEventLimit || len(raw) > syncLineLimit {
		return nil, errSyncProtocol
	}
	var fields map[string]json.RawMessage
	if strictSyncJSON(raw, &fields) != nil {
		return nil, errSyncProtocol
	}
	for _, key := range []string{"protocol", "job_id", "source_id", "operation_id", "seq", "type"} {
		if _, ok := fields[key]; !ok {
			return nil, errSyncProtocol
		}
	}
	var header syncEventHeader
	// Header is read from the closed event below; this preliminary extraction
	// only chooses its closed schema and never accepts arbitrary fields.
	if json.Unmarshal(fields["type"], &header.Type) != nil {
		return nil, errSyncProtocol
	}
	next := *p.job
	var canonical []byte
	switch header.Type {
	case "run_started":
		var event syncStartedEvent
		if strictSyncEventJSON(raw, &event, header.Type) != nil {
			return nil, errSyncProtocol
		}
		header = event.syncEventHeader
		canonical, _ = json.Marshal(event)
		if old, ok := p.seen[header.Seq]; ok {
			if old == syncDigest(canonical) {
				return nil, nil
			}
			return nil, errSyncProtocol
		}
		if next.Cutoff != nil || header.Seq != 1 || event.Mode != "incremental" || event.BridgeVersion != next.BridgeVersion || event.ComponentRef != next.ComponentManifestRef || event.ConfigRevision != next.ConfigRevision || event.ScopeDigest != next.ScopeDigest || len(event.ExpectedShopIDs) != 7 {
			return nil, errSyncProtocol
		}
		for i, shop := range event.ExpectedShopIDs {
			if shop != next.ExpectedShops[i] {
				return nil, errSyncProtocol
			}
		}
		cutoff, err := time.Parse(time.RFC3339, event.Cutoff)
		if err != nil || !strings.HasSuffix(event.Cutoff, "+08:00") || len(event.Cutoff) != 25 || cutoff.Nanosecond() != 0 || cutoff.After(time.Now()) || cutoff.Before(next.CreatedAt.Truncate(time.Second)) || !validExpectedStates(&next, event.ExpectedStates, cutoff) {
			return nil, errSyncProtocol
		}
		before := make(map[string]time.Time, 14)
		for _, s := range next.ExpectedStates {
			before[syncStateKey(s.Source, s.Entity, s.ShopID)] = s.BeforeWatermark
		}
		for _, s := range event.ExpectedStates {
			if old, ok := before[syncStateKey(s.Source, s.Entity, s.ShopID)]; !ok || !old.Equal(s.BeforeWatermark) {
				return nil, errSyncProtocol
			}
		}
		next.Cutoff = &cutoff
		next.Stage = "syncing"
		p.cutoffText = event.Cutoff
	case "shop_finished":
		for _, key := range []string{"shop_id", "status", "order_source", "orders_count", "aftersales_occurrence_count", "related_orders_refetch_completed", "safe_error_code"} {
			if _, ok := fields[key]; !ok {
				return nil, errSyncProtocol
			}
		}
		var event syncShopEvent
		if strictSyncEventJSON(raw, &event, header.Type) != nil {
			return nil, errSyncProtocol
		}
		header = event.syncEventHeader
		canonical, _ = json.Marshal(event)
		if old, ok := p.seen[header.Seq]; ok {
			if old == syncDigest(canonical) {
				return nil, nil
			}
			return nil, errSyncProtocol
		}
		result := event.ERPSyncShopResult
		if next.Cutoff == nil || p.finished || next.SourceMapping[result.ShopID] == "" || next.SourceMapping[result.ShopID] != result.OrderSource || result.OrdersCount < 0 || result.AftersalesOccurrenceCount < 0 || (result.Status != "ok" && result.Status != "failed") || len(result.SafeErrorCode) > 64 {
			return nil, errSyncProtocol
		}
		if result.Status == "ok" && (!result.RelatedOrdersRefetchCompleted || result.SafeErrorCode != "") {
			return nil, errSyncProtocol
		}
		if result.Status == "failed" && result.SafeErrorCode != syncSafeShopCode(result.SafeErrorCode) {
			return nil, errSyncProtocol
		}
		if previous, ok := p.shops[result.ShopID]; ok {
			if previous != result {
				return nil, errSyncProtocol
			}
		} else {
			p.shops[result.ShopID] = result
			next.ShopResults = append(append([]models.ERPSyncShopResult(nil), next.ShopResults...), result)
		}
	case "run_finished":
		for _, key := range []string{"cutoff", "expected_count", "processed_count", "succeeded_count", "failed_count", "shop_results_digest"} {
			if _, ok := fields[key]; !ok {
				return nil, errSyncProtocol
			}
		}
		var event syncFinishedEvent
		if strictSyncEventJSON(raw, &event, header.Type) != nil {
			return nil, errSyncProtocol
		}
		header = event.syncEventHeader
		canonical, _ = json.Marshal(event)
		if old, ok := p.seen[header.Seq]; ok {
			if old == syncDigest(canonical) {
				return nil, nil
			}
			return nil, errSyncProtocol
		}
		okCount := 0
		for _, result := range p.shops {
			if result.Status == "ok" {
				okCount++
			}
		}
		if next.Cutoff == nil || p.finished || event.Cutoff != p.cutoffText || event.ExpectedCount != 7 || event.ProcessedCount != 7 || len(p.shops) != 7 || event.SucceededCount != okCount || event.FailedCount != 7-okCount || event.ShopResultsDigest != syncShopDigest(next.ShopResults) {
			return nil, errSyncProtocol
		}
		p.finished = true
		next.RunFinished = true
		next.Stage = "verifying"
	default:
		return nil, errSyncProtocol
	}
	if header.Protocol != erpBridgeProtocol || header.JobID != next.JobID || header.SourceID != next.SourceID || header.OperationID != next.OperationID || header.Seq != next.EventSeq+1 {
		return nil, errSyncProtocol
	}
	p.seen[header.Seq] = syncDigest(canonical)
	next.EventSeq = header.Seq
	next.Revision++
	return &next, nil
}

// proc identity is read from stat field 22, not a PID alone. A pidfd pins every
// signal target. Descendants additionally prove the inherited source inode FD3.
type syncProcStat struct {
	pid, pgid int
	start     uint64
	state     byte
}

func readSyncProcStat(pid int) (syncProcStat, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return syncProcStat{}, err
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return syncProcStat{}, errSyncProcess
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return syncProcStat{}, errSyncProcess
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return syncProcStat{}, errSyncProcess
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return syncProcStat{}, errSyncProcess
	}
	return syncProcStat{pid: pid, pgid: pgid, start: start, state: fields[0][0]}, nil
}
func syncProcessIdentity(pid int, source, operation, digest string) (*models.ERPSyncProcess, error) {
	stat, err := readSyncProcStat(pid)
	if err != nil || stat.pgid != pid {
		return nil, errSyncProcess
	}
	file, err := os.Open(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return nil, errSyncProcess
	}
	actual, err := syncHashFile(file)
	file.Close()
	if err != nil || actual != digest {
		return nil, errSyncProcess
	}
	boot, err := syncBootID()
	if err != nil {
		return nil, err
	}
	mnt, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return nil, errSyncProcess
	}
	pidns, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid))
	if err != nil {
		return nil, errSyncProcess
	}
	return &models.ERPSyncProcess{PID: pid, PGID: stat.pgid, StartTicks: stat.start, BootID: boot, MountNamespace: mnt, PIDNamespace: pidns, ExecutableSHA256: actual, SourceID: source, OperationID: operation}, nil
}

func syncGroupMembers(identity *models.ERPSyncProcess) ([]syncProcStat, error) {
	boot, err := syncBootID()
	if err != nil {
		return nil, err
	}
	if boot != identity.BootID {
		return nil, nil
	}
	mnt, err := syncNamespace("mnt")
	if err != nil {
		return nil, err
	}
	pidns, err := syncNamespace("pid")
	if err != nil || mnt != identity.MountNamespace || pidns != identity.PIDNamespace {
		return nil, errSyncProcess
	}
	leader, err := readSyncProcStat(identity.PID)
	if err == nil && leader.start != identity.StartTicks {
		return nil, errSyncProcess
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, errSyncProcess
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, errSyncProcess
	}
	var members []syncProcStat
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := readSyncProcStat(pid)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errSyncProcess
		}
		if stat.pgid != identity.PGID || stat.state == 'Z' || stat.state == 'X' {
			continue
		}
		if stat.start < identity.StartTicks {
			return nil, errSyncProcess
		}
		members = append(members, stat)
	}
	return members, nil
}

// A pidfd names the exact observed process even after its /proc files vanish.
// Only an exited target may bypass a disappearing exe/FD observation below.
func syncPidfdExited(fd int) bool {
	poll := [1]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll[:], 0)
	return err == nil && n == 1 && poll[0].Revents&unix.POLLIN != 0
}

func syncSignalGroup(source *models.ERPSource, identity *models.ERPSyncProcess, signal unix.Signal) error {
	members, err := syncGroupMembers(identity)
	if err != nil {
		return err
	}
	for _, member := range members {
		fd, err := unix.PidfdOpen(member.pid, 0)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return errSyncProcess
		}
		current, err := readSyncProcStat(member.pid)
		if os.IsNotExist(err) && syncPidfdExited(fd) {
			unix.Close(fd)
			continue
		}
		if err != nil || current.start != member.start || current.pgid != identity.PGID {
			unix.Close(fd)
			return errSyncProcess
		}
		if member.pid == identity.PID {
			file, openErr := os.Open(fmt.Sprintf("/proc/%d/exe", member.pid))
			if openErr != nil {
				exited := os.IsNotExist(openErr) && syncPidfdExited(fd)
				unix.Close(fd)
				if exited {
					continue
				}
				return errSyncProcess
			}
			digest, hashErr := syncHashFile(file)
			file.Close()
			if hashErr != nil || digest != identity.ExecutableSHA256 {
				unix.Close(fd)
				return errSyncProcess
			}
		}
		if member.pid != identity.PID {
			var st unix.Stat_t
			statErr := unix.Stat(fmt.Sprintf("/proc/%d/fd/3", member.pid), &st)
			if os.IsNotExist(statErr) && syncPidfdExited(fd) {
				unix.Close(fd)
				continue
			}
			if statErr != nil || uint64(st.Dev) != source.LockDevice || st.Ino != source.LockInode {
				unix.Close(fd)
				return errSyncProcess
			}
		}
		err = unix.PidfdSendSignal(fd, signal, nil, 0)
		unix.Close(fd)
		if err != nil && !errors.Is(err, unix.ESRCH) {
			return errSyncProcess
		}
	}
	return nil
}

func waitSyncGroup(ctx context.Context, identity *models.ERPSyncProcess, budget time.Duration) bool {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		members, err := syncGroupMembers(identity)
		if err != nil {
			return false
		}
		if len(members) == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}
func (r *processERPSyncRunner) stop(source *models.ERPSource, identity *models.ERPSyncProcess) bool {
	if identity == nil {
		return false
	}
	if syncSignalGroup(source, identity, unix.SIGTERM) != nil {
		return false
	}
	if waitSyncGroup(context.Background(), identity, r.termBudget) {
		return true
	}
	if syncSignalGroup(source, identity, unix.SIGKILL) != nil {
		return false
	}
	return waitSyncGroup(context.Background(), identity, r.killBudget)
}

func (r *processERPSyncRunner) Recover(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease) error {
	if lease != nil {
		if err := syncLease(source, job, lease); err != nil {
			return err
		}
	} else {
		// Startup may stop a proven orphan while its FD3 still owns EX. This
		// grants no execution/clear permission: EX and PG observation follow.
		if source == nil || job == nil || source.Phase != models.ERPSourceRecovering || !job.MatchesOperation(source.ActiveOperation) || source.ActiveOperation.InstanceID != job.InstanceID || source.SourceID != job.SourceID {
			return errSyncProcess
		}
	}
	if job == nil || job.Process == nil {
		return nil
	}
	if job.Process.SourceID != source.SourceID || job.Process.OperationID != job.OperationID || job.Process.PID < 2 || job.Process.PGID != job.Process.PID || job.Process.StartTicks == 0 {
		return errSyncProcess
	}
	members, err := syncGroupMembers(job.Process)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return nil
	}
	if ctx.Err() != nil || !r.stop(source, job.Process) {
		return errSyncProcess
	}
	return nil
}

func (r *processERPSyncRunner) Run(ctx context.Context, source *models.ERPSource, job *models.DataSyncJob, lease *sourcegate.Lease, launch *erpSyncLaunch, persist func(*models.DataSyncJob) error) erpSyncRunResult {
	result := erpSyncRunResult{SafeCode: "RUNNER_FAILED"}
	if syncLease(source, job, lease) != nil || launch == nil {
		return result
	}
	inherited, err := lease.InheritFile(lease.Identity())
	if err != nil {
		return result
	}
	defer inherited.Close()
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return result
	}
	defer stdoutRead.Close()
	defer stdoutWrite.Close()
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		return result
	}
	defer stderrRead.Close()
	defer stderrWrite.Close()
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return result
	}
	defer stdinRead.Close()
	defer stdinWrite.Close()
	cmd := exec.Command(launch.executable, launch.args...)
	cmd.Dir = launch.directory
	cmd.Env = launch.environment
	cmd.ExtraFiles = []*os.File{inherited}
	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderrWrite
	cmd.Stdin = stdinRead
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	// exec's Linux pdeathsig uses the launching thread. The process owner stays
	// on that thread until Wait; a thread exit must not kill unrelated work.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		result.ExitConfirmed = true
		return result
	}
	inherited.Close()
	stdoutWrite.Close()
	stderrWrite.Close()
	stdinRead.Close()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	identity, err := syncProcessIdentity(cmd.Process.Pid, job.SourceID, job.OperationID, launch.executableDigest)
	if err != nil {
		// The child has received no start frame. Closing stdin plus pdeathsig
		// cannot authorize writes; never signal a PID with unproved identity.
		stdinWrite.Close()
		select {
		case <-waited:
		case <-time.After(r.startBudget):
		}
		return result
	}
	copyJob := *job
	copyJob.Process = identity
	copyJob.Status = models.DataSyncRunning
	copyJob.Revision++
	now := time.Now().UTC()
	copyJob.StartedAt = &now
	if err := persist(&copyJob); err != nil {
		stdinWrite.Close()
		result.ExitConfirmed = r.stop(source, identity)
		select {
		case <-waited:
		case <-time.After(r.killBudget):
			result.ExitConfirmed = false
		}
		return result
	}
	*job = copyJob
	frame := syncStartFrame{Protocol: erpBridgeProtocol, Type: "start", JobID: job.JobID, SourceID: job.SourceID, OperationID: job.OperationID, InstanceID: job.InstanceID, Mode: "incremental", LockFD: 3, ComponentRef: job.ComponentManifestRef, ComponentDigest: job.ComponentDigest, BridgeVersion: job.BridgeVersion, ConfigRef: source.SecretInjectionRef, ConfigRevision: job.ConfigRevision, ScopeDigest: job.ScopeDigest, ExpectedShopIDs: job.ExpectedShops, SourceMapping: job.SourceMapping, AftersaleSource: job.AftersaleSource, ExpectedStates: job.ExpectedStates}
	frameBytes, _ := json.Marshal(frame)
	frameBytes = append(frameBytes, '\n')
	if len(frameBytes) > syncLineLimit {
		stdinWrite.Close()
		result.ExitConfirmed = r.stop(source, identity)
		select {
		case <-waited:
		case <-time.After(r.killBudget):
			result.ExitConfirmed = false
		}
		return result
	}
	written := make(chan error, 1)
	go func() { _, err := stdinWrite.Write(frameBytes); stdinWrite.Close(); written <- err }()
	select {
	case err = <-written:
	case <-time.After(r.startBudget):
		err = errSyncProtocol
		stdinWrite.Close()
	case <-ctx.Done():
		err = ctx.Err()
		stdinWrite.Close()
	}
	if err != nil {
		result.ExitConfirmed = r.stop(source, identity)
		select {
		case <-waited:
		case <-time.After(r.killBudget):
			result.ExitConfirmed = false
		}
		return result
	}
	copyJob = *job
	processCopy := *identity
	processCopy.StartFrameConfirmed = true
	copyJob.Process = &processCopy
	copyJob.Revision++
	if persist(&copyJob) != nil {
		result.ExitConfirmed = r.stop(source, identity)
		select {
		case <-waited:
		case <-time.After(r.killBudget):
			result.ExitConfirmed = false
		}
		return result
	}
	*job = copyJob
	var total atomic.Int64
	outputFailure := make(chan string, 1)
	failOutput := func(code string) {
		select {
		case outputFailure <- code:
		default:
		}
	}
	abortOutput := make(chan struct{})
	ctxDone := ctx.Done()
	lines := make(chan []byte, 8)
	var readers sync.WaitGroup
	readers.Add(2)
	go func(output chan<- []byte) {
		defer readers.Done()
		defer close(output)
		var line []byte
		buffer := make([]byte, 4096)
		discard := false
		for {
			n, readErr := stdoutRead.Read(buffer)
			if n > 0 {
				if total.Add(int64(n)) > syncOutputLimit {
					failOutput("RUNNER_OUTPUT_LIMIT")
					discard = true
				}
				for _, b := range buffer[:n] {
					if discard {
						continue
					}
					if b == '\n' {
						copyLine := append([]byte(nil), line...)
						line = line[:0]
						select {
						case output <- copyLine:
						case <-abortOutput:
							discard = true
						}
					} else {
						line = append(line, b)
						if len(line) > syncLineLimit {
							failOutput("RUNNER_OUTPUT_LIMIT")
							discard = true
							line = nil
						}
					}
				}
			}
			if readErr != nil {
				select {
				case <-abortOutput:
					// Stop deliberately closes these pipes; a cut frame is not
					// another failure or evidence of an output budget violation.
					return
				default:
				}
				if readErr != io.EOF {
					failOutput("RUNNER_FAILED")
				} else if len(line) != 0 {
					failOutput("RUNNER_PROTOCOL_INVALID")
				}
				return
			}
		}
	}(lines)
	go func() {
		defer readers.Done()
		buffer := make([]byte, 4096)
		lineLength := 0
		for {
			n, readErr := stderrRead.Read(buffer)
			if n > 0 {
				if total.Add(int64(n)) > syncOutputLimit {
					failOutput("RUNNER_OUTPUT_LIMIT")
				}
				for _, b := range buffer[:n] {
					if b == '\n' {
						lineLength = 0
					} else {
						lineLength++
						if lineLength > syncLineLimit {
							failOutput("RUNNER_OUTPUT_LIMIT")
							lineLength = syncLineLimit + 1
						}
					}
				}
			}
			if readErr != nil {
				select {
				case <-abortOutput:
					return
				default:
				}
				if readErr != io.EOF {
					failOutput("RUNNER_FAILED")
				}
				return
			}
		}
	}()
	readDone := make(chan struct{})
	go func() { readers.Wait(); close(readDone) }()
	protocol := newSyncProtocol(job)
	timer := time.NewTimer(r.runBudget)
	defer timer.Stop()
	failed := false
	waitDone := false
	outputDone := false
	var waitErr error
	stop := func(code string) {
		if failed {
			return
		}
		failed = true
		close(abortOutput)
		result.SafeCode = code
		// A stop attempt is provisional. Preserve the cause; joined Wait and
		// the final identity/group observation decide whether exit is proven.
		r.stop(source, identity)
		stdoutRead.Close()
		stderrRead.Close()
	}
	for !waitDone || !outputDone {
		select {
		case raw, open := <-lines:
			if !open {
				lines = nil
				continue
			}
			if failed {
				continue
			}
			next, eventErr := protocol.accept(raw)
			if eventErr != nil {
				stop("RUNNER_PROTOCOL_INVALID")
				continue
			}
			if next != nil {
				if persist(next) != nil {
					stop("SYNC_STORAGE_UNAVAILABLE")
				} else {
					*job = *next
				}
			}
		case waitErr = <-waited:
			waitDone = true
			waited = nil
			if !waitSyncGroup(context.Background(), identity, r.killBudget) {
				stop("RUNNER_FAILED")
			}
		case code := <-outputFailure:
			stop(code)
		case <-timer.C:
			stop("RUNNER_TIMEOUT")
			timer.Reset(r.killBudget)
		case <-ctxDone:
			stop("RUNNER_INTERRUPTED")
			ctxDone = nil
		case <-readDone:
			outputDone = true
			readDone = nil
		}
		if failed && !waitDone {
			select {
			case waitErr = <-waited:
				waitDone = true
				waited = nil
			case <-time.After(r.killBudget):
				result.SafeCode = "RUNNER_PROCESS_UNCERTAIN"
				return result
			}
		}
	}
	// Drain events queued before readers completed; a Wait notification must
	// not turn missing or buffered tail events into success.
	if lines != nil {
		for raw := range lines {
			if !failed {
				next, eventErr := protocol.accept(raw)
				if eventErr != nil {
					failed = true
					result.SafeCode = "RUNNER_PROTOCOL_INVALID"
				} else if next != nil {
					if persist(next) != nil {
						failed = true
						result.SafeCode = "SYNC_STORAGE_UNAVAILABLE"
					} else {
						*job = *next
					}
				}
			}
		}
	}
	select {
	case code := <-outputFailure:
		if !failed {
			failed = true
			result.SafeCode = code
		}
	default:
	}
	code := 0
	waitKnown := waitErr == nil
	if waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			code = exit.ExitCode()
			waitKnown = true
		} else {
			code = -1
		}
	}
	result.ExitCode = &code
	members, groupErr := syncGroupMembers(identity)
	result.ExitConfirmed = waitDone && waitKnown && groupErr == nil && len(members) == 0
	if !result.ExitConfirmed {
		result.SafeCode = "RUNNER_PROCESS_UNCERTAIN"
		return result
	}
	if !failed && protocol.finished && len(job.ShopResults) == 7 && code == 0 {
		allOK := true
		for _, shop := range job.ShopResults {
			if shop.Status != "ok" || !shop.RelatedOrdersRefetchCompleted {
				allOK = false
			}
		}
		if allOK {
			result.SafeCode = ""
		} else {
			result.SafeCode = "SHOP_FAILED"
		}
	} else if !failed {
		if code != 0 {
			result.SafeCode = "RUNNER_FAILED"
		} else {
			result.SafeCode = "RUNNER_PROTOCOL_INVALID"
		}
	}
	return result
}
