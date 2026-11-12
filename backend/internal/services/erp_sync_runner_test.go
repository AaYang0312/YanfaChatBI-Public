package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"golang.org/x/sys/unix"
	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/sourcegate"
)

func erpSyncProbeJob() *models.DataSyncJob {
	job := &models.DataSyncJob{SchemaVersion: 1, JobID: uuid.NewString(), SourceID: uuid.NewString(), InitiatingOwnerID: primitive.NewObjectID(), InitiatingChatID: primitive.NewObjectID(), RequestID: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64), RunnerVersion: models.DataSyncRunnerVersion, Mode: "incremental", ComponentManifestRef: "test-only-probe", ComponentDigest: strings.Repeat("b", 64), BridgeVersion: "test-probe/1", ConfigRevision: 1, ScopeDigest: strings.Repeat("c", 64), ExpectedShops: []string{"1", "2", "3", "4", "5", "6", "7"}, SourceMapping: map[string]string{"1": "test.orders", "2": "test.orders", "3": "test.orders", "4": "test.orders", "5": "test.orders", "6": "test.orders", "7": "test.orders"}, AftersaleSource: "test.aftersales", InstanceID: "test-process-instance", OperationID: uuid.NewString(), BindingRevision: 1, ConnectionFingerprint: strings.Repeat("d", 64), Status: models.DataSyncCreated, Stage: "preparing", CreatedAt: time.Now().UTC()}
	states, _ := expectedSyncStates(job)
	for i := range states {
		states[i].BeforeWatermark = time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	}
	job.ExpectedStates = states
	return job
}
func erpSyncProbeLaunch(t *testing.T, scenario, record string) *erpSyncLaunch {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("manager prerequisite: Python 3 required for isolated probe")
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(python)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := syncHashFile(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	probe, err := filepath.Abs("testdata/erp_bridge_probe.py")
	if err != nil {
		t.Fatal(err)
	}
	return &erpSyncLaunch{executable: python, directory: filepath.Dir(probe), args: []string{"-u", probe}, environment: []string{"YANFA_TEST_ONLY_PROBE=1", "YANFA_PROBE_CASE=" + scenario, "YANFA_PROBE_RECORD=" + record, "PYTHONNOUSERSITE=1", "LANG=C.UTF-8"}, executableDigest: digest}
}
func erpSyncProbeGate(t *testing.T, job *models.DataSyncJob) (*sourcegate.Gate, *models.ERPSource, *sourcegate.Lease) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })
	target, err := gate.Provision(job.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	source := &models.ERPSource{SourceID: job.SourceID, LockDevice: target.Device, LockInode: target.Inode, DeploymentUID: uint32(os.Getuid())}
	lease, err := gate.Acquire(context.Background(), target, sourcegate.Exclusive, jobSyncIdentity(job))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	return gate, source, lease
}
func waitERPSyncFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			return raw
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actual subprocess observation not produced")
	return nil
}
func requireERPSyncGate(t *testing.T, gate *sourcegate.Gate, source *models.ERPSource, wantBusy bool) {
	t.Helper()
	lease, err := gate.Acquire(context.Background(), sourceTarget(source), sourcegate.Exclusive, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
	if lease != nil {
		lease.Close()
	}
	if wantBusy && !errors.Is(err, sourcegate.ErrBusy) {
		t.Fatalf("writer lost inherited exclusion: %v", err)
	}
	if !wantBusy && err != nil {
		t.Fatalf("actual last FD not released: %v", err)
	}
}

func TestERPSyncRunnerRealProtocolAndBounds(t *testing.T) {
	for _, scenario := range []string{"ok", "duplicate", "duplicate_shop", "conflicting_seq", "conflicting_shop", "missing_tail", "missing_shop", "shop_error_exit0", "wrong_protocol", "wrong_source", "out_of_order", "unknown_field", "future_cutoff", "null_required", "after_tail", "line_limit", "output_limit", "fork_descendant", "case_alias_count_conflict", "case_alias_required_only", "case_alias_nested_state", "case_alias_seq_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			job := erpSyncProbeJob()
			gate, source, lease := erpSyncProbeGate(t, job)
			record := filepath.Join(t.TempDir(), "start.jsonl")
			runner := NewERPSyncRunner(config.ERPSyncRuntime{RunSeconds: 5, TermSeconds: 1, KillSeconds: 1}, uint32(os.Getuid()))
			result := runner.Run(context.Background(), source, job, lease, erpSyncProbeLaunch(t, scenario, record), func(next *models.DataSyncJob) error {
				if next.Process != nil && job.Process == nil {
					if _, err := os.Stat(record); !os.IsNotExist(err) {
						t.Error("start frame preceded durable process identity callback")
					}
					if next.Process.PID < 2 || next.Process.PGID != next.Process.PID || next.Process.BootID == "" || next.Process.StartTicks == 0 {
						t.Error("missing actual process identity")
					}
				}
				requireERPSyncGate(t, gate, source, true)
				return nil
			})
			if !result.ExitConfirmed {
				t.Fatalf("real process group did not finish: %s", result.SafeCode)
			}
			good := scenario == "ok" || scenario == "duplicate" || scenario == "duplicate_shop"
			if good && (result.SafeCode != "" || !job.RunFinished || len(job.ShopResults) != 7) {
				t.Fatalf("valid full batch rejected: %+v", result)
			}
			if strings.HasPrefix(scenario, "case_alias_") && result.SafeCode != "RUNNER_PROTOCOL_INVALID" {
				t.Fatalf("case-alias wire frame was not rejected as a protocol failure: scenario=%s safe_code=%q run_finished=%t event_seq=%d", scenario, result.SafeCode, job.RunFinished, job.EventSeq)
			}
			if !good && result.SafeCode == "" {
				t.Fatal("invalid/partial/over-limit/descendant batch succeeded")
			}
			if scenario == "shop_error_exit0" && (result.ExitCode == nil || *result.ExitCode != 0 || result.SafeCode != "SHOP_FAILED") {
				t.Fatal("exit0 shop error was not independently rejected")
			}
			raw, _ := json.Marshal(job)
			if strings.Contains(string(raw), "synthetic-private-canary") {
				t.Fatal("raw stderr reached persisted model")
			}
			requireERPSyncGate(t, gate, source, true)
			lease.Close()
			requireERPSyncGate(t, gate, source, false)
		})
	}
}

func TestERPSyncRunnerRejectsReusedIdentityWithoutSignal(t *testing.T) {
	job := erpSyncProbeJob()
	_, source, lease := erpSyncProbeGate(t, job)
	stat, err := readSyncProcStat(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	boot, err := syncBootID()
	if err != nil {
		t.Fatal(err)
	}
	mnt, _ := syncNamespace("mnt")
	pidns, _ := syncNamespace("pid")
	job.Process = &models.ERPSyncProcess{PID: os.Getpid(), PGID: os.Getpid(), BootID: boot, StartTicks: stat.start + 1, SourceID: job.SourceID, OperationID: job.OperationID, MountNamespace: mnt, PIDNamespace: pidns, ExecutableSHA256: strings.Repeat("0", 64)}
	runner := NewERPSyncRunner(config.ERPSyncRuntime{}, uint32(os.Getuid()))
	if runner.Recover(context.Background(), source, job, lease) == nil {
		t.Fatal("reused PID/start identity was accepted")
	}
	if _, err := readSyncProcStat(os.Getpid()); err != nil {
		t.Fatal("recovery signalled an unowned process")
	}
}

func TestERPSyncRunnerParentDeath(t *testing.T) {
	if os.Getenv("YANFA_PARENT_HELPER") == "1" {
		var job models.DataSyncJob
		var source models.ERPSource
		if json.Unmarshal([]byte(os.Getenv("YANFA_PARENT_JOB")), &job) != nil || json.Unmarshal([]byte(os.Getenv("YANFA_PARENT_SOURCE")), &source) != nil {
			t.Fatal("invalid helper fixture")
		}
		gate, err := sourcegate.New(os.Getenv("YANFA_PARENT_DIRECTORY"), uint32(os.Getuid()))
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		lease, err := gate.Acquire(context.Background(), sourceTarget(&source), sourcegate.Exclusive, jobSyncIdentity(&job))
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		runner := NewERPSyncRunner(config.ERPSyncRuntime{RunSeconds: 30, TermSeconds: 1, KillSeconds: 1}, uint32(os.Getuid()))
		runner.Run(context.Background(), &source, &job, lease, erpSyncProbeLaunch(t, "hold", os.Getenv("YANFA_PARENT_RECORD")), func(next *models.DataSyncJob) error {
			raw, _ := json.Marshal(next.Process)
			return os.WriteFile(os.Getenv("YANFA_PARENT_IDENTITY"), raw, 0600)
		})
		return
	}
	job := erpSyncProbeJob()
	gate, source, lease := erpSyncProbeGate(t, job)
	lease.Close()
	directory := t.TempDir()
	record := filepath.Join(directory, "child.jsonl")
	identityPath := filepath.Join(directory, "identity.json")
	jobRaw, _ := json.Marshal(job)
	sourceRaw, _ := json.Marshal(source)
	cmd := exec.Command(os.Args[0], "-test.run=^TestERPSyncRunnerParentDeath$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "YANFA_PARENT_HELPER=1", "YANFA_PARENT_JOB=" + string(jobRaw), "YANFA_PARENT_SOURCE=" + string(sourceRaw), "YANFA_PARENT_DIRECTORY=" + gate.Directory(), "YANFA_PARENT_RECORD=" + record, "YANFA_PARENT_IDENTITY=" + identityPath}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	waitERPSyncFile(t, record)
	requireERPSyncGate(t, gate, source, true)
	var identity models.ERPSyncProcess
	if json.Unmarshal(waitERPSyncFile(t, identityPath), &identity) != nil {
		t.Fatal("no persisted child identity")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if !waitSyncGroup(context.Background(), &identity, 5*time.Second) {
		t.Fatal("actual pdeath child survived parent death")
	}
	requireERPSyncGate(t, gate, source, false)
}

func TestERPSyncRunnerLastInheritedReference(t *testing.T) {
	job := erpSyncProbeJob()
	gate, source, lease := erpSyncProbeGate(t, job)
	record := filepath.Join(t.TempDir(), "child.jsonl")
	runner := NewERPSyncRunner(config.ERPSyncRuntime{RunSeconds: 3, TermSeconds: 1, KillSeconds: 1}, uint32(os.Getuid()))
	result := runner.Run(context.Background(), source, job, lease, erpSyncProbeLaunch(t, "escaped_fd", record), func(*models.DataSyncJob) error { return nil })
	if result.SafeCode == "" {
		t.Fatal("escaped incomplete batch succeeded")
	}
	raw := waitERPSyncFile(t, record+".escaped")
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	defer unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	var st unix.Stat_t
	if unix.Stat(filepath.Join("/proc", string(raw), "fd/3"), &st) != nil || st.Ino != source.LockInode || uint64(st.Dev) != source.LockDevice {
		t.Fatal("escaped child did not retain original FD3")
	}
	lease.Close()
	requireERPSyncGate(t, gate, source, true)
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probe, err := gate.Acquire(context.Background(), sourceTarget(source), sourcegate.Exclusive, sourcegate.Identity{SourceID: source.SourceID, OperationID: uuid.NewString()})
		if err == nil {
			probe.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("last inherited file description was not released after actual child death")
}

func TestERPSyncRunnerAbortPreservesFailureCause(t *testing.T) {
	for _, scenario := range []struct {
		name, code string
		termReply  bool
	}{
		{"abort_partial_stdout", "RUNNER_PROTOCOL_INVALID", true},
		{"abort_partial_stderr", "RUNNER_PROTOCOL_INVALID", true},
		{"partial_stdout_eof", "RUNNER_PROTOCOL_INVALID", false},
		{"stderr_total_limit", "RUNNER_OUTPUT_LIMIT", false},
		{"line_limit", "RUNNER_OUTPUT_LIMIT", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			job := erpSyncProbeJob()
			gate, source, lease := erpSyncProbeGate(t, job)
			record := filepath.Join(t.TempDir(), "abort-start.jsonl")
			runner := NewERPSyncRunner(config.ERPSyncRuntime{RunSeconds: 5, TermSeconds: 1, KillSeconds: 1}, uint32(os.Getuid()))
			var result erpSyncRunResult
			t.Cleanup(func() {
				if !result.ExitConfirmed && job.Process != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := runner.Recover(ctx, source, job, lease); err != nil {
						t.Error("actual abort child could not be safely recovered during cleanup")
					}
				}
			})
			result = runner.Run(context.Background(), source, job, lease, erpSyncProbeLaunch(t, scenario.name, record), func(*models.DataSyncJob) error {
				requireERPSyncGate(t, gate, source, true)
				return nil
			})
			if !result.ExitConfirmed || result.ExitCode == nil || job.Process == nil || !job.Process.StartFrameConfirmed {
				t.Fatalf("actual abort did not finish its persisted start-frame/Wait/group path: code=%q exit_confirmed=%t", result.SafeCode, result.ExitConfirmed)
			}
			if result.SafeCode != scenario.code {
				t.Errorf("actual abort/drain replaced the initiating failure: scenario=%s code=%q expected=%q exit_confirmed=%t", scenario.name, result.SafeCode, scenario.code, result.ExitConfirmed)
			}
			var started struct {
				PID       int    `json:"pid"`
				Operation string `json:"operation_id"`
				Device    uint64 `json:"device"`
				Inode     uint64 `json:"inode"`
			}
			if json.Unmarshal(waitERPSyncFile(t, record), &started) != nil || started.PID != job.Process.PID || started.Operation != job.OperationID || started.Device != source.LockDevice || started.Inode != source.LockInode {
				t.Fatal("abort evidence did not come from the actual child with inherited source FD3")
			}
			if scenario.termReply {
				var terminated struct {
					PID    int    `json:"pid"`
					Signal string `json:"signal"`
				}
				if json.Unmarshal(waitERPSyncFile(t, record+".terminated"), &terminated) != nil || terminated.PID != started.PID || terminated.Signal != "TERM" || *result.ExitCode != 0 {
					t.Fatal("partial-tail child did not wait for actual parent termination and exit cleanly")
				}
			}
			if job.RunFinished || job.EventSeq != 0 || len(job.ShopResults) != 0 {
				t.Error("invalid/partial/over-budget child advanced a valid protocol batch")
			}
			requireERPSyncGate(t, gate, source, true)
			lease.Close()
			requireERPSyncGate(t, gate, source, false)
			t.Logf("actual abort result: scenario=%s safe_code=%q exit_code=%d exit_confirmed=%t", scenario.name, result.SafeCode, *result.ExitCode, result.ExitConfirmed)
		})
	}
}

// The original parent runs the actual installed T45 code. Only this test
// composes HTTP transport and a real writer-close barrier; it emits no events.
func TestERPSyncT45OriginalParentAndFailedRefetch(t *testing.T) {
	python, artifact := os.Getenv("YANFA_SYNC_TEST_PYTHON"), os.Getenv("YANFA_SYNC_T45_ARTIFACT")
	if python == "" || artifact == "" || os.Getenv("YANFA_SYNC_TEST_ALLOW_RESET") != "owned-18545-bi_agent" {
		t.Fatal("manager must supply the actual isolated T45 artifact/runtime and reset consent")
	}
	f, err := os.Open(artifact)
	if err != nil {
		t.Fatal("actual artifact missing")
	}
	digest, err := syncHashFile(f)
	f.Close()
	if err != nil || digest != "077c3be10a76bc9251f68660db1e2e55b08807c077ddaaf615547d4d0ce3d282" {
		t.Fatal("producer artifact differs")
	}
	f, err = os.Open(artifact)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(gz)
	modules := map[string]string{}
	for {
		h, e := archive.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if strings.HasPrefix(h.Name, "bi_agent/") && strings.HasSuffix(h.Name, ".py") {
			raw, e := io.ReadAll(archive)
			if e != nil {
				t.Fatal(e)
			}
			modules[h.Name] = syncDigest(raw)
		}
	}
	gz.Close()
	f.Close()
	if len(modules) != 12 {
		t.Fatal("actual minimal module closure differs")
	}
	component, err := filepath.Abs("../../../components/erp-sync")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "refetch-failure"} {
		t.Run(scenario, func(t *testing.T) {
			seed := fmt.Sprintf(`import sys,json,hashlib,importlib
from pathlib import Path
from datetime import datetime,timedelta,timezone
sys.path.insert(0,%q)
import test_bridge_lifecycle as lifecycle
from test_bridge_protocol import make_start
before=(datetime.now(timezone(timedelta(hours=8)))-timedelta(days=1)).replace(hour=23,minute=50,second=0,microsecond=595293)
lifecycle.make_start=lambda:make_start(before)
lifecycle.BridgeLifecycleTests.setUpClass()
case=lifecycle.BridgeLifecycleTests(methodName='test_real_seven_shop_pagination_commits_and_one_cutoff')
case.setUp()
origins={}
for name in %s:
 module=importlib.import_module(name[:-3].replace('/','.').removesuffix('.__init__'))
 origins[name]=hashlib.sha256(Path(module.__file__).read_bytes()).hexdigest()
print(json.dumps({'frame':case.frame,'origins':origins}))
`, filepath.Join(component, "tests"), func() string {
				names := make([]string, 0, len(modules))
				for name := range modules {
					names = append(names, name)
				}
				raw, _ := json.Marshal(names)
				return string(raw)
			}())
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			seedCmd := exec.CommandContext(ctx, python, "-I", "-B", "-c", seed)
			seedCmd.Env = os.Environ()
			raw, e := seedCmd.Output()
			if e != nil {
				if ee, ok := e.(*exec.ExitError); ok {
					t.Fatalf("actual isolated component fixture failed: %s", ee.Stderr)
				}
				t.Fatal("actual isolated component fixture failed")
			}
			var seeded struct {
				Frame   syncStartFrame    `json:"frame"`
				Origins map[string]string `json:"origins"`
			}
			if json.Unmarshal(raw, &seeded) != nil {
				t.Fatal("fixture metadata unavailable")
			}
			for name, want := range modules {
				if seeded.Origins[name] != want {
					t.Fatal("installed component bytes differ from producer artifact")
				}
			}
			source := &models.ERPSource{SourceID: uuid.NewString(), ConfigRevision: 1, ComponentManifestRef: seeded.Frame.ComponentRef, SecretInjectionRef: seeded.Frame.ConfigRef, ShopScope: seeded.Frame.ExpectedShopIDs, OrderSourceByShop: seeded.Frame.SourceMapping, DeploymentUID: uint32(os.Getuid()), PhysicalDatabase: "bi_agent", ReaderRoleRefs: []string{"synthetic-reader"}}
			job, e := freezeSyncJob(source, nil, uuid.NewString(), "synthetic-t40-real-parent", seeded.Frame.ComponentDigest, seeded.Frame.BridgeVersion, seeded.Frame.AftersaleSource)
			if e != nil {
				t.Fatal(e)
			}
			job.ExpectedStates = seeded.Frame.ExpectedStates
			gate, locked, lease := erpSyncProbeGate(t, job)
			source.LockDevice, source.LockInode = locked.LockDevice, locked.LockInode
			barrier, release := filepath.Join(t.TempDir(), "writer-close"), filepath.Join(t.TempDir(), "continue")
			wrapper := fmt.Sprintf(`import runpy,time
from pathlib import Path
h=runpy.run_path(%q)
h['install_transport'](%q)
from bi_agent import sync
original=sync._connect
def connect(dsn):
 writer=original(dsn)
 close=writer.close
 def joined_close():
  Path(%q).write_text('writer-held')
  while not Path(%q).exists():time.sleep(.01)
  close()
 writer.close=joined_close
 return writer
sync._connect=connect
from bi_agent.yanfa_bridge import main
raise SystemExit(main(['incremental']))
`, filepath.Join(component, "tests/bridge_process_child.py"), scenario, barrier, release)
			pythonFile, e := os.Open(python)
			if e != nil {
				t.Fatal(e)
			}
			pythonHash, e := syncHashFile(pythonFile)
			pythonFile.Close()
			if e != nil {
				t.Fatal(e)
			}
			environment := []string{"BI_WRITER_DSN=" + os.Getenv("YANFA_SYNC_TEST_WRITER_DSN"), "BI_SHOP_IDS=" + strings.Join(job.ExpectedShops, ","), "KUAI_MAI_APP_KEY=synthetic-key", "KUAI_MAI_APP_SECRET=synthetic-secret", "KUAI_MAI_ACCESS_TOKEN=synthetic-token", "KUAI_MAI_REFRESH_TOKEN=synthetic-refresh", "PYTHONSAFEPATH=1", "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1", "LANG=C.UTF-8", "TZ=Asia/Shanghai"}
			launch := &erpSyncLaunch{executable: python, directory: t.TempDir(), args: []string{"-I", "-B", "-u", "-c", wrapper}, environment: environment, executableDigest: pythonHash}
			runner := NewERPSyncRunner(config.ERPSyncRuntime{RunSeconds: 30, TermSeconds: 1, KillSeconds: 1}, uint32(os.Getuid()))
			tailHeld := false
			defer os.WriteFile(release, []byte("continue"), 0600)
			result := runner.Run(ctx, source, job, lease, launch, func(next *models.DataSyncJob) error {
				if next.RunFinished && !tailHeld {
					waitERPSyncFile(t, barrier)
					requireERPSyncGate(t, gate, source, true)
					db, e := sql.Open("postgres", os.Getenv("YANFA_SYNC_TEST_CONTROL_DSN"))
					if e != nil {
						return e
					}
					db.SetMaxIdleConns(0)
					var acquired bool
					e = db.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(7319041)").Scan(&acquired)
					db.Close()
					if e != nil || acquired {
						return errors.New("tail released actual writer PG before close")
					}
					tailHeld = true
					if os.WriteFile(release, []byte("continue"), 0600) != nil {
						return errors.New("close barrier release failed")
					}
				}
				return nil
			})
			if !tailHeld || !result.ExitConfirmed || !job.RunFinished || len(job.ShopResults) != 7 || result.ExitCode == nil {
				exitCode := -1
				if result.ExitCode != nil {
					exitCode = *result.ExitCode
				}
				startConfirmed := job.Process != nil && job.Process.StartFrameConfirmed
				t.Fatalf("actual parent did not join real T45 lifecycle: safe_code=%s exit_confirmed=%t exit_code=%d start_confirmed=%t event_seq=%d shop_count=%d run_finished=%t tail_held=%t", result.SafeCode, result.ExitConfirmed, exitCode, startConfirmed, job.EventSeq, len(job.ShopResults), job.RunFinished, tailHeld)
			}
			if scenario == "success" && (result.SafeCode != "" || *result.ExitCode != 0) {
				t.Fatal("actual component success rejected")
			}
			if scenario == "refetch-failure" && (result.SafeCode == "" || *result.ExitCode == 0 || job.ShopResults[0].Status != "failed" || job.ShopResults[0].RelatedOrdersRefetchCompleted) {
				t.Fatal("watermark completion erased refetch failure")
			}
			home, _ := os.UserHomeDir()
			private, e := os.MkdirTemp(home, ".t40-component-control-")
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(private)
			controlPath := filepath.Join(private, "control.json")
			controlRaw, _ := json.Marshal(map[string]string{"dsn": os.Getenv("YANFA_SYNC_TEST_CONTROL_DSN")})
			if os.WriteFile(controlPath, controlRaw, 0600) != nil {
				t.Fatal("control fixture")
			}
			rt := config.ERPSourceRuntime{ControlReader: config.SecretFileReference{Reference: "synthetic-component-control", Path: controlPath, UID: uint32(os.Getuid()), Mode: 0600}, ReaderRoleByRef: map[string]string{"synthetic-reader": "v15_bridge_reader"}, WriterRole: "v15_bridge_writer", ControlRole: "v15_bridge_control", ObservationApprovalRef: "synthetic-only"}
			verification, verifyErr := NewERPSyncVerifier(config.ERPSyncRuntime{Sources: map[string]config.ERPSourceRuntime{source.SourceID: rt}}, job.InstanceID).Verify(ctx, source, job, lease)
			if verifyErr != nil || verification == nil || !verification.Passed || len(verification.Rows) != 14 {
				t.Fatal("actual 14-key read-only verification failed")
			}
			if scenario == "refetch-failure" && result.SafeCode == "" {
				t.Fatal("14 watermarks incorrectly overrode failed refetch")
			}
			lease.Close()
			requireERPSyncGate(t, gate, source, false)
		})
	}
}
