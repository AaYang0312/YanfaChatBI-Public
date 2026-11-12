//go:build linux

package sourcegate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func sourceGateFixture(t *testing.T) (*Gate, Target, Identity) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	gate, err := New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })
	target, err := gate.Provision(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return gate, target, Identity{SourceID: target.SourceID, OperationID: uuid.NewString(), OwnerID: "owner", ChatID: "chat", BindingRevision: 1, ConnectionFingerprint: "endpoint", ConfigRevision: 1}
}

func acquireGate(t *testing.T, gate *Gate, target Target, mode Mode, identity Identity) *Lease {
	t.Helper()
	lease, err := gate.Acquire(context.Background(), target, mode, identity)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

// Runs only in an explicitly constructed test subprocess; no inherited backend
// environment or production lock directory is used.
func TestSourceGateProcessHelper(t *testing.T) {
	mode := os.Getenv("YANFA_GATE_TEST_MODE")
	if mode == "" {
		return
	}
	if mode == "inherit" {
		file := os.NewFile(3, "inherited-source-ex")
		if _, err := file.Stat(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("ready")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		file.Close()
		return
	}
	var target Target
	if err := json.Unmarshal([]byte(os.Getenv("YANFA_GATE_TEST_TARGET")), &target); err != nil {
		t.Fatal(err)
	}
	gate, err := New(os.Getenv("YANFA_GATE_TEST_DIRECTORY"), uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	lockMode := Shared
	if mode == "writer" {
		lockMode = Exclusive
	}
	lease, err := gate.Acquire(context.Background(), target, lockMode, Identity{SourceID: target.SourceID, OperationID: uuid.NewString()})
	if errors.Is(err, ErrBusy) {
		fmt.Println("busy")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	fmt.Println("ready")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
}

type gateTestProcess struct {
	command *exec.Cmd
	finish  func()
	status  string
}

func startGateProcess(t *testing.T, directory string, target Target, mode string, inherited *os.File) *gateTestProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceGateProcessHelper$")
	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	command.Env = []string{"YANFA_GATE_TEST_MODE=" + mode, "YANFA_GATE_TEST_DIRECTORY=" + directory, "YANFA_GATE_TEST_TARGET=" + string(encoded)}
	if inherited != nil {
		command.ExtraFiles = []*os.File{inherited}
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	status, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		cancel()
		command.Wait()
		t.Fatalf("child handshake: %v %s", err, stderr.String())
	}
	finished := false
	finish := func() {
		if finished {
			return
		}
		finished = true
		if strings.TrimSpace(status) == "ready" {
			_, _ = input.Write([]byte("exit\n"))
		}
		input.Close()
		err := command.Wait()
		cancel()
		if err != nil {
			t.Errorf("child exit: %v %s", err, stderr.String())
		}
	}
	t.Cleanup(finish)
	return &gateTestProcess{command: command, finish: finish, status: strings.TrimSpace(status)}
}

func TestSourceGateProcessContention(t *testing.T) {
	gate, target, _ := sourceGateFixture(t)
	first := startGateProcess(t, gate.directory, target, "reader", nil)
	second := startGateProcess(t, gate.directory, target, "reader", nil)
	if first.status != "ready" || second.status != "ready" {
		t.Fatal("independent readers did not coexist")
	}
	writer := startGateProcess(t, gate.directory, target, "writer", nil)
	if writer.status != "busy" {
		t.Fatal("writer entered while reader processes held SH")
	}
	writer.finish()
	first.finish()
	second.finish()
	writer = startGateProcess(t, gate.directory, target, "writer", nil)
	if writer.status != "ready" {
		t.Fatal("writer did not enter after all readers exited")
	}
	reader := startGateProcess(t, gate.directory, target, "reader", nil)
	if reader.status != "busy" {
		t.Fatal("reader entered while writer process held EX")
	}
	reader.finish()
	otherWriter := startGateProcess(t, gate.directory, target, "writer", nil)
	if otherWriter.status != "busy" {
		t.Fatal("second writer entered")
	}
	otherWriter.finish()
	writer.finish()
}

func TestSourceGateInheritedFileDescription(t *testing.T) {
	gate, target, identity := sourceGateFixture(t)
	lease := acquireGate(t, gate, target, Exclusive, identity)
	file, err := lease.InheritFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	child := startGateProcess(t, gate.directory, target, "inherit", file)
	if child.status != "ready" {
		t.Fatal("child did not inherit FD3")
	}
	file.Close()
	lease.Close()
	if acquired, err := gate.Acquire(context.Background(), target, Exclusive, identity); !errors.Is(err, ErrBusy) {
		if acquired != nil {
			acquired.Close()
		}
		t.Fatalf("parent close unlocked inherited OFD: %v", err)
	}
	child.finish()
	acquireGate(t, gate, target, Exclusive, identity).Close()
}

func TestSourceGateRetainCancellationAndJoin(t *testing.T) {
	gate, target, identity := sourceGateFixture(t)
	root := acquireGate(t, gate, target, Exclusive, identity)
	ctx, cancel := context.WithCancel(WithLease(context.Background(), root))
	childCtx, child, err := RetainContext(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	finish := make(chan struct{})
	finished := make(chan struct{})
	go func() { defer close(finished); defer child.Close(); <-finish; <-childCtx.Done() }()
	cancel()
	root.Close()
	joined := make(chan struct{})
	go func() { root.Join(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("cancellation joined an actual running child")
	default:
	}
	if acquired, err := gate.Acquire(context.Background(), target, Exclusive, identity); !errors.Is(err, ErrBusy) {
		if acquired != nil {
			acquired.Close()
		}
		t.Fatalf("cancel prematurely released EX: %v", err)
	}
	close(finish)
	<-finished
	<-joined
	acquireGate(t, gate, target, Exclusive, identity).Close()
	if _, err := child.Retain(); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed child retained: %v", err)
	}
}

func TestSourceGateRejectsIdentityAndReplacement(t *testing.T) {
	gate, target, identity := sourceGateFixture(t)
	lease := acquireGate(t, gate, target, Exclusive, identity)
	wrong := identity
	wrong.OperationID = uuid.NewString()
	if _, err := FromContext(WithLease(context.Background(), lease), wrong); !errors.Is(err, ErrIdentity) {
		t.Fatalf("cross operation read: %v", err)
	}
	wrong = identity
	wrong.BindingRevision++
	if _, err := FromContext(WithLease(context.Background(), lease), wrong); !errors.Is(err, ErrIdentity) {
		t.Fatalf("cross binding read: %v", err)
	}
	wrong = identity
	wrong.SourceID = uuid.NewString()
	if _, err := lease.InheritFile(wrong); !errors.Is(err, ErrIdentity) {
		t.Fatalf("cross source inheritance: %v", err)
	}
	lease.Close()
	path := filepath.Join(gate.directory, target.SourceID+".lock")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if acquired, err := gate.Acquire(context.Background(), target, Shared, identity); !errors.Is(err, ErrUnsafePath) {
		if acquired != nil {
			acquired.Close()
		}
		t.Fatalf("replacement inode admitted: %v", err)
	}
}

func TestSourceGateRejectsUnsafeDirectoryAndLockFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if gate, err := New(directory, uint32(os.Getuid())); !errors.Is(err, ErrUnsafePath) {
		if gate != nil {
			gate.Close()
		}
		t.Fatalf("public gate directory accepted: %v", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, symlink); err != nil {
		t.Fatal(err)
	}
	if gate, err := New(symlink, uint32(os.Getuid())); !errors.Is(err, ErrUnsafePath) {
		if gate != nil {
			gate.Close()
		}
		t.Fatalf("symlink directory accepted: %v", err)
	}
	gate, target, identity := sourceGateFixture(t)
	path := filepath.Join(gate.directory, target.SourceID+".lock")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if lease, err := gate.Acquire(context.Background(), target, Shared, identity); !errors.Is(err, ErrUnsafePath) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("public lock file accepted: %v", err)
	}
}
