//go:build linux

// Package sourcegate owns the single source exclusion boundary. Cancellation
// cancels work; only the last reference close releases a lease.
package sourcegate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

var (
	ErrBusy       = errors.New("SOURCE_BUSY")
	ErrIdentity   = errors.New("SOURCE_LEASE_MISMATCH")
	ErrMissing    = errors.New("SOURCE_LEASE_MISSING")
	ErrClosed     = errors.New("SOURCE_LEASE_CLOSED")
	ErrUnsafePath = errors.New("SOURCE_GATE_UNSAFE_PATH")
)

type Mode uint8

const (
	Shared Mode = iota + 1
	Exclusive
)

// Target pins a provisioned file, including across backend restarts. Persist
// Device/Inode in the source registration; never recreate a missing lock file.
type Target struct {
	SourceID string
	Device   uint64
	Inode    uint64
}

// Identity is frozen by the server after ownership/binding authorization.
// A binding operation may carry a prospective revision while holding EX.
type Identity struct {
	SourceID              string
	OperationID           string
	OwnerID               string
	ChatID                string
	BindingRevision       int64
	ConnectionFingerprint string
	ConfigRevision        int64
}

type Gate struct {
	mu        sync.Mutex
	directory string
	uid       uint32
	dir       *os.File
	device    uint64
	inode     uint64
}

// New requires an already provisioned 0700 directory owned by the deployment
// UID. It neither creates directories nor follows symlinks in any component.
func New(directory string, uid uint32) (*Gate, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || uint32(os.Geteuid()) != uid {
		return nil, ErrUnsafePath
	}
	fd, err := openDirectory(directory)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), directory)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Uid != uid || st.Mode&07777 != 0700 {
		f.Close()
		return nil, ErrUnsafePath
	}
	return &Gate{directory: directory, uid: uid, dir: f, device: uint64(st.Dev), inode: st.Ino}, nil
}

func openDirectory(path string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, ErrUnsafePath
		}
		fd = next
	}
	return fd, nil
}

func (g *Gate) directoryFD() (int, error) {
	g.mu.Lock()
	if g.dir == nil {
		g.mu.Unlock()
		return -1, ErrClosed
	}
	fd, err := unix.FcntlInt(g.dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	g.mu.Unlock()
	if err != nil {
		return -1, err
	}
	current, err := openDirectory(g.directory)
	if err != nil {
		unix.Close(fd)
		return -1, err
	}
	var st unix.Stat_t
	err = unix.Fstat(current, &st)
	unix.Close(current)
	if err != nil || uint64(st.Dev) != g.device || st.Ino != g.inode || st.Uid != g.uid || st.Mode&07777 != 0700 {
		unix.Close(fd)
		return -1, ErrUnsafePath
	}
	return fd, nil
}

func validSource(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u != uuid.Nil && u.String() == id
}

func (g *Gate) open(sourceID string, create bool) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	if !validSource(sourceID) {
		return nil, st, ErrIdentity
	}
	dirfd, err := g.directoryFD()
	if err != nil {
		return nil, st, err
	}
	defer unix.Close(dirfd)
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	name := sourceID + ".lock"
	fd, err := unix.Openat(dirfd, name, flags, 0600)
	if create && errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dirfd, name, flags & ^(unix.O_CREAT|unix.O_EXCL), 0)
	}
	if err != nil {
		return nil, st, ErrUnsafePath
	}
	f := os.NewFile(uintptr(fd), name)
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != g.uid || st.Mode&07777 != 0600 || st.Nlink != 1 {
		f.Close()
		return nil, st, ErrUnsafePath
	}
	return f, st, nil
}

// Provision is only for approved registration, before exposing a source. Its
// result must be persisted; Acquire never provisions or changes an inode.
func (g *Gate) Provision(sourceID string) (Target, error) {
	f, st, err := g.open(sourceID, true)
	if err != nil {
		return Target{}, err
	}
	if err := f.Close(); err != nil {
		return Target{}, err
	}
	return Target{SourceID: sourceID, Device: uint64(st.Dev), Inode: st.Ino}, nil
}

func (g *Gate) Acquire(ctx context.Context, target Target, mode Mode, identity Identity) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if identity.SourceID != target.SourceID || !validSource(identity.OperationID) || target.Inode == 0 || (mode != Shared && mode != Exclusive) {
		return nil, ErrIdentity
	}
	f, st, err := g.open(target.SourceID, false)
	if err != nil {
		return nil, err
	}
	if uint64(st.Dev) != target.Device || st.Ino != target.Inode {
		f.Close()
		return nil, ErrUnsafePath
	}
	op := unix.LOCK_SH | unix.LOCK_NB
	if mode == Exclusive {
		op = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), op); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		f.Close()
		return nil, err
	}
	state := &references{file: f, count: 1, done: make(chan struct{})}
	state.changed = sync.NewCond(&state.mu)
	return &Lease{state: state, identity: identity, mode: mode}, nil
}

func (g *Gate) Close() error {
	g.mu.Lock()
	f := g.dir
	g.dir = nil
	g.mu.Unlock()
	if f == nil {
		return nil
	}
	return f.Close()
}

type references struct {
	mu      sync.Mutex
	changed *sync.Cond
	file    *os.File
	count   int
	done    chan struct{}
}

// Lease cannot be constructed with usable fields outside this package.
// Every retained handle is closed exactly once, independently of its siblings.
type Lease struct {
	state    *references
	identity Identity
	mode     Mode
	closed   bool // protected by state.mu
}

func (l *Lease) Identity() Identity { return l.identity }
func (l *Lease) Mode() Mode         { return l.mode }

func (l *Lease) Check(identity Identity) error {
	if l == nil || l.state == nil {
		return ErrIdentity
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.closed || l.state.file == nil {
		return ErrClosed
	}
	if l.identity != identity {
		return ErrIdentity
	}
	return nil
}

func (l *Lease) Retain() (*Lease, error) {
	if l == nil || l.state == nil {
		return nil, ErrIdentity
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.closed || l.state.file == nil {
		return nil, ErrClosed
	}
	l.state.count++
	return &Lease{state: l.state, identity: l.identity, mode: l.mode}, nil
}

func (l *Lease) Close() error {
	if l == nil || l.state == nil {
		return nil
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.state.count--
	l.state.changed.Broadcast()
	if l.state.count != 0 {
		return nil
	}
	err := l.state.file.Close() // Never LOCK_UN: descendants may inherit this OFD.
	l.state.file = nil
	close(l.state.done)
	return err
}

// Join observes actual completion, without a deadline that could unlock work.
// Close the caller's handle before Join; retained descendants close their own.
func (l *Lease) Join() {
	if l != nil && l.state != nil {
		<-l.state.done
	}
}

// JoinDescendants keeps this handle live while waiting for all sibling/child
// handles to close. Only reference completion wakes it; cancellation never does.
func (l *Lease) JoinDescendants() error {
	if l == nil || l.state == nil {
		return ErrIdentity
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	for !l.closed && l.state.count > 1 {
		l.state.changed.Wait()
	}
	if l.closed || l.state.file == nil {
		return ErrClosed
	}
	return nil
}

// InheritFile returns a dup of the same open-file-description for cmd.ExtraFiles.
// The caller closes the duplicate after Start (or Start failure), and retains a
// Lease until Wait plus descendant/session verification has completed.
func (l *Lease) InheritFile(identity Identity) (*os.File, error) {
	if l == nil || l.state == nil {
		return nil, ErrIdentity
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.closed || l.state.file == nil {
		return nil, ErrClosed
	}
	if l.mode != Exclusive || l.identity != identity {
		return nil, ErrIdentity
	}
	fd, err := unix.FcntlInt(l.state.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), fmt.Sprintf("source-%s", identity.SourceID)), nil
}

type leaseKey struct{ sourceID string }
type currentLeaseKey struct{}

func WithLease(ctx context.Context, lease *Lease) context.Context {
	ctx = context.WithValue(ctx, currentLeaseKey{}, lease)
	return context.WithValue(ctx, leaseKey{lease.identity.SourceID}, lease)
}

// HasLease identifies an admitted ERP context without granting access to it.
func HasLease(ctx context.Context) bool {
	_, exists := ctx.Value(currentLeaseKey{}).(*Lease)
	return exists
}

// FromContext checks all frozen identity fields, including operation and binding.
// It accepts the operation's own EX for required reads, without taking a SH.
func FromContext(ctx context.Context, identity Identity) (*Lease, error) {
	lease, ok := ctx.Value(leaseKey{identity.SourceID}).(*Lease)
	if !ok {
		return nil, ErrIdentity
	}
	if err := lease.Check(identity); err != nil {
		return nil, err
	}
	return lease, nil
}

// RetainContext must be called before launching a goroutine. Cancellation of ctx
// is preserved but does not close the retained handle. The actual worker closes it.
func RetainContext(ctx context.Context, identity Identity) (context.Context, *Lease, error) {
	lease, err := FromContext(ctx, identity)
	if err != nil {
		return nil, nil, err
	}
	child, err := lease.Retain()
	if err != nil {
		return nil, nil, err
	}
	return WithLease(ctx, child), child, nil
}

// Directory and UID identify the pinned deployment namespace, not busy state.
func (g *Gate) Directory() string { return g.directory }
func (g *Gate) UID() uint32       { return g.uid }

func ContextIdentity(ctx context.Context, sourceID string) (Identity, error) {
	lease, ok := ctx.Value(leaseKey{sourceID}).(*Lease)
	if !ok {
		return Identity{}, ErrMissing
	}
	if err := lease.Check(lease.identity); err != nil {
		return Identity{}, err
	}
	return lease.identity, nil
}
