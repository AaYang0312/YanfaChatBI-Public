package erpsyncinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yanfachatbi/config"

	"golang.org/x/sys/unix"
)

const Recipe = "yanfa-minimal-bridge-frozen-wheels/1"
const LockSHA256 = "2acc27c959019b0761726578111e7558992e8cf9632a7800b5c6e2fcf135ae43"
const UVVersion = "0.13.7"

type Request struct {
	StagingDirectory        string                      `json:"staging_directory"`
	ReleaseDirectory        string                      `json:"release_directory"`
	ArtifactPath            string                      `json:"artifact_path"`
	ArtifactSHA256          string                      `json:"artifact_sha256"`
	UVExecutablePath        string                      `json:"uv_executable_path"`
	UVVersion               string                      `json:"uv_version"`
	UVSHA256                string                      `json:"uv_sha256"`
	ToolOriginReceipt       string                      `json:"tool_origin_receipt"`
	PythonExecutablePath    string                      `json:"python_executable_path"`
	PythonVersion           string                      `json:"python_version"`
	PythonSHA256            string                      `json:"python_sha256"`
	Wheelhouse              string                      `json:"wheelhouse"`
	WheelhouseOriginReceipt string                      `json:"wheelhouse_origin_receipt"`
	RequirementsSHA256      string                      `json:"requirements_sha256"`
	Manifest                config.ERPComponentManifest `json:"manifest"`
}
type Preparation struct {
	ArtifactSHA256          string            `json:"artifact_sha256"`
	RequirementsSHA256      string            `json:"requirements_sha256"`
	UVSHA256                string            `json:"uv_sha256"`
	PythonSHA256            string            `json:"python_sha256"`
	ToolOriginReceipt       string            `json:"tool_origin_receipt"`
	WheelhouseOriginReceipt string            `json:"wheelhouse_origin_receipt"`
	InstalledWheelSHA256    map[string]string `json:"installed_wheel_sha256"`
	PreparedFiles           map[string]string `json:"prepared_files"`
	RequestSHA256           string            `json:"request_sha256"`
}
type VerificationRequest struct {
	ManifestPath   string `json:"manifest_path"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ReceiptPath    string `json:"receipt_path"`
	ReceiptSHA256  string `json:"receipt_sha256"`
	DeploymentUID  uint32 `json:"deployment_uid"`
}

type boundedOutput struct {
	bytes.Buffer
	exceeded bool
	max      int
}

func (w *boundedOutput) Write(raw []byte) (int, error) {
	n := len(raw)
	if n > w.max-w.Len() {
		w.exceeded = true
		return n, nil
	}
	w.Buffer.Write(raw)
	return n, nil
}
func run(ctx context.Context, executable, directory string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = directory
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TZ=Asia/Shanghai", "PYTHONSAFEPATH=1", "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1", "UV_OFFLINE=1", "UV_NO_CACHE=1", "UV_PYTHON_DOWNLOADS=never", "HOME=" + directory, "TMPDIR=" + directory}
	out := &boundedOutput{max: 1 << 20}
	errout := &boundedOutput{max: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = errout
	if cmd.Run() != nil || out.exceeded || errout.exceeded {
		return nil, errors.New("ERP_INSTALL_COMMAND_FAILED")
	}
	return out.Bytes(), nil
}
func checkedExecutable(path, expected string, uid uint32) error {
	f, err := OpenFile(path, uid, 0, false)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Mode()&0111 == 0 {
		return ErrUnsafe
	}
	hash, err := FileDigest(f)
	if err != nil || hash != expected {
		return ErrInput
	}
	return nil
}
func validateRequest(req Request) error {
	if !filepath.IsAbs(req.StagingDirectory) || filepath.Clean(req.StagingDirectory) != req.StagingDirectory || !strings.HasPrefix(req.ReleaseDirectory, "/opt/yanfa/erp-sync/releases/") || filepath.Dir(req.ReleaseDirectory) != "/opt/yanfa/erp-sync/releases" || filepath.Clean(req.ReleaseDirectory) != req.ReleaseDirectory || req.ToolOriginReceipt == "" || req.WheelhouseOriginReceipt == "" || len(req.ArtifactSHA256) != 64 || len(req.RequirementsSHA256) != 64 {
		return ErrInput
	}
	if req.UVVersion != UVVersion {
		return errors.New("UV_TOOL_VERSION_MISMATCH")
	}
	m := req.Manifest
	if m.ExecutableAbsolutePath != req.ReleaseDirectory+"/python/bin/python" || m.WorkingDirectory != req.ReleaseDirectory || m.ArtifactSHA256 != req.ArtifactSHA256 || m.LockSHA256 != LockSHA256 || m.InstallRecipe != Recipe || m.UVVersion != UVVersion || m.PythonVersion != req.PythonVersion || m.BridgeProtocol != "yanfa-erp-bridge/1" || m.BridgeVersion != "yanfa-bridge/0.1.0" || m.ComponentOwnerRef == "" || m.RepositoryURI == "" || m.CommitOrRelease == "" || m.ConfigRef == "" || m.ApprovedBy == "" || len(m.MaintenanceOperations) > 1 || len(m.MaintenanceOperations) == 1 && m.MaintenanceOperations[0] != "incremental" {
		return ErrInput
	}
	if _, err := time.Parse(time.RFC3339, m.ApprovedAt); err != nil {
		return ErrInput
	}
	return nil
}
func requireEmptyDirectory(path string, uid uint32) error {
	fd, err := openDirectory(path, uid, false)
	if err != nil {
		return err
	}
	defer unixClose(fd)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0700 {
		return ErrUnsafe
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		return ErrUnsafe
	}
	return nil
}

// unixClose is kept local to directory ownership; it never unlocks a source lease.
func unixClose(fd int) { f := os.NewFile(uintptr(fd), "erp-directory"); f.Close() }
func sourceMembers() map[string]bool {
	m := map[string]bool{"README.md": true, "source-input.json": true, "build-recipe.json": true, "dependency-recipe/pyproject.toml": true, "dependency-recipe/uv.lock": true, "dependency-recipe/package_release.py": true}
	for _, name := range []string{"__init__.py", "sync.py", "config.py", "data_quality.py", "kuaimai.py", "metrics.py", "sources.py", "catalog/__init__.py", "catalog/models.py", "catalog/projection.py", "catalog/repository.py", "yanfa_bridge.py"} {
		m["bi_agent/"+name] = true
	}
	return m
}
func extractArtifact(raw []byte, directory string) error {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return ErrInput
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	allowed := sourceMembers()
	seen := map[string]bool{}
	total := int64(0)
	directories := map[string]bool{}
	for name := range allowed {
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			directories[filepath.ToSlash(parent)+"/"] = true
		}
	}
	seenDirectories := map[string]bool{}
	for {
		h, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrInput
		}
		if h.Typeflag == tar.TypeDir {
			if !directories[h.Name] || seenDirectories[h.Name] || h.Size != 0 {
				return errors.New("SOURCE_INPUT_UNSAFE")
			}
			seenDirectories[h.Name] = true
			continue
		}
		if !allowed[h.Name] || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 8<<20 {
			return errors.New("SOURCE_INPUT_UNSAFE")
		}
		total += h.Size
		if total > 32<<20 {
			return ErrInput
		}
		seen[h.Name] = true
		path := filepath.Join(directory, filepath.FromSlash(h.Name))
		if os.MkdirAll(filepath.Dir(path), 0700) != nil {
			return ErrUnsafe
		}
		body, err := io.ReadAll(io.LimitReader(archive, h.Size+1))
		if err != nil || int64(len(body)) != h.Size {
			return ErrInput
		}
		if WriteExclusive(path, body, 0644) != nil {
			return ErrUnsafe
		}
	}
	if len(seen) != len(allowed) {
		return errors.New("SOURCE_INPUT_MISSING")
	}
	lock, err := os.ReadFile(filepath.Join(directory, "dependency-recipe/uv.lock"))
	if err != nil || Digest(lock) != LockSHA256 {
		return errors.New("SOURCE_INPUT_HASH_MISMATCH")
	}
	return nil
}
func wheelInputs(requirements []byte, directory string, uid uint32) (map[string]string, error) {
	fd, err := openDirectory(directory, uid, false)
	if err != nil {
		return nil, err
	}
	unixClose(fd)
	lines := strings.Split(strings.ReplaceAll(string(requirements), "\\\n", " "), "\n")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, ErrInput
	}
	selected := map[string]string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hashStart := strings.Index(line, "--hash=sha256:")
		if hashStart < 0 {
			return nil, ErrInput
		}
		requirement := strings.TrimSpace(line[:hashStart])
		pin, marker, hasMarker := strings.Cut(requirement, ";")
		pin = strings.TrimSpace(pin)
		// The fixed original lock has exactly this conditional binary pin. The
		// checked CPython input makes it applicable; no expression is evaluated.
		if hasMarker && strings.TrimSpace(marker) != "implementation_name != 'pypy'" && strings.TrimSpace(marker) != "implementation_name != \"pypy\"" {
			return nil, ErrInput
		}
		name, version, ok := strings.Cut(pin, "==")
		if !ok || strings.ContainsAny(name+version, "/\\$; ") {
			return nil, ErrInput
		}
		prefix := strings.ReplaceAll(strings.ToLower(name), "-", "_") + "-" + version + "-"
		allowed := map[string]bool{}
		for _, f := range strings.Fields(line[hashStart:]) {
			if !strings.HasPrefix(f, "--hash=sha256:") || len(f) != len("--hash=sha256:")+64 {
				return nil, ErrInput
			}
			allowed[strings.TrimPrefix(f, "--hash=sha256:")] = true
		}
		found := false
		for _, entry := range entries {
			if !strings.HasPrefix(strings.ToLower(entry.Name()), prefix) || !strings.HasSuffix(entry.Name(), ".whl") {
				continue
			}
			f, err := OpenFile(filepath.Join(directory, entry.Name()), uid, 0, false)
			if err != nil {
				return nil, err
			}
			hash, e := FileDigest(f)
			f.Close()
			if e != nil || !allowed[hash] {
				return nil, errors.New("LOCKED_WHEEL_HASH_MISMATCH")
			}
			selected[entry.Name()] = hash
			found = true
		}
		if !found {
			return nil, fmt.Errorf("LOCKED_WHEEL_MISSING:%s", pin)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("LOCKED_WHEEL_MISSING")
	}
	return selected, nil
}
func Prepare(ctx context.Context, req Request) (*Preparation, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	uid := uint32(os.Geteuid())
	if err := requireEmptyDirectory(req.StagingDirectory, uid); err != nil {
		return nil, err
	}
	if checkedExecutable(req.UVExecutablePath, req.UVSHA256, uid) != nil {
		return nil, errors.New("UV_TOOL_INPUT_MISSING")
	}
	if checkedExecutable(req.PythonExecutablePath, req.PythonSHA256, uid) != nil {
		return nil, errors.New("PYTHON_TOOL_INPUT_MISSING")
	}
	version, err := run(ctx, req.UVExecutablePath, req.StagingDirectory, "--version")
	versionFields := strings.Fields(string(version))
	if err != nil || len(versionFields) < 2 || versionFields[0] != "uv" || versionFields[1] != UVVersion {
		return nil, errors.New("UV_TOOL_VERSION_MISMATCH")
	}
	version, err = run(ctx, req.PythonExecutablePath, req.StagingDirectory, "-I", "-B", "--version")
	if err != nil || strings.TrimSpace(string(version)) != "Python "+req.PythonVersion {
		return nil, errors.New("PYTHON_TOOL_VERSION_MISMATCH")
	}
	raw, err := ReadFile(req.ArtifactPath, uid, 32<<20)
	if err != nil || Digest(raw) != req.ArtifactSHA256 {
		return nil, errors.New("SOURCE_INPUT_HASH_MISMATCH")
	}
	source := filepath.Join(req.StagingDirectory, "source")
	if os.Mkdir(source, 0700) != nil {
		return nil, ErrUnsafe
	}
	if err := extractArtifact(raw, source); err != nil {
		return nil, err
	}
	requirements, err := run(ctx, req.UVExecutablePath, filepath.Join(source, "dependency-recipe"), "export", "--frozen", "--no-dev", "--no-emit-project", "--no-header", "--no-annotate")
	if err != nil {
		return nil, err
	}
	if Digest(requirements) != req.RequirementsSHA256 {
		return nil, errors.New("LOCKED_REQUIREMENTS_HASH_MISMATCH")
	}
	wheels, err := wheelInputs(requirements, req.Wheelhouse, uid)
	if err != nil {
		return nil, err
	}
	if WriteExclusive(filepath.Join(req.StagingDirectory, "requirements.txt"), requirements, 0600) != nil {
		return nil, ErrUnsafe
	}
	pythonDirectory := filepath.Join(req.StagingDirectory, "python")
	if _, err := run(ctx, req.PythonExecutablePath, req.StagingDirectory, "-I", "-B", "-m", "venv", "--copies", "--without-pip", pythonDirectory); err != nil {
		return nil, err
	}
	// CPython venv adds an unused lib64 -> lib convenience alias. It is not
	// installed: the release tree contains regular files and real directories.
	alias := filepath.Join(pythonDirectory, "lib64")
	if target, err := os.Readlink(alias); err == nil {
		if target != "lib" || os.Remove(alias) != nil {
			return nil, ErrUnsafe
		}
	}
	python := filepath.Join(pythonDirectory, "bin/python")
	if _, err := run(ctx, req.UVExecutablePath, req.StagingDirectory, "pip", "install", "--python", python, "--require-hashes", "--only-binary=:all:", "--no-deps", "--no-index", "--find-links", req.Wheelhouse, "-r", filepath.Join(req.StagingDirectory, "requirements.txt")); err != nil {
		return nil, err
	}
	if err := removeUVEnvironmentLock(pythonDirectory, uid); err != nil {
		return nil, err
	}
	location, err := run(ctx, python, req.StagingDirectory, "-I", "-B", "-c", "import sysconfig; print(sysconfig.get_path('purelib'))")
	if err != nil {
		return nil, err
	}
	site := strings.TrimSpace(string(location))
	if !strings.HasPrefix(site, pythonDirectory+"/") || filepath.Clean(site) != site {
		return nil, ErrUnsafe
	}
	if err := copyTree(filepath.Join(source, "bi_agent"), filepath.Join(site, "bi_agent"), uid); err != nil {
		return nil, err
	}
	files, err := inventory(req.StagingDirectory, uid, false)
	if err != nil {
		return nil, err
	}
	requestRaw, _ := json.Marshal(req)
	result := &Preparation{ArtifactSHA256: req.ArtifactSHA256, RequirementsSHA256: req.RequirementsSHA256, UVSHA256: req.UVSHA256, PythonSHA256: req.PythonSHA256, ToolOriginReceipt: req.ToolOriginReceipt, WheelhouseOriginReceipt: req.WheelhouseOriginReceipt, InstalledWheelSHA256: wheels, PreparedFiles: files, RequestSHA256: Digest(requestRaw)}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	if WriteExclusive(filepath.Join(req.StagingDirectory, "preparation.json"), append(encoded, '\n'), 0600) != nil {
		return nil, ErrUnsafe
	}
	return result, nil
}

// uv leaves an empty coordination file in the venv after its child exits.
// It is not part of the runtime and can be created with mode 0666.
func removeUVEnvironmentLock(pythonDirectory string, uid uint32) error {
	dir, err := openDirectory(pythonDirectory, uid, false)
	if err != nil {
		return err
	}
	defer unixClose(dir)
	var lock unix.Stat_t
	if err := unix.Fstatat(dir, ".lock", &lock, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil || lock.Mode&unix.S_IFMT != unix.S_IFREG || lock.Uid != uid || lock.Nlink != 1 || lock.Size != 0 {
		return ErrUnsafe
	}
	if unix.Unlinkat(dir, ".lock", 0) != nil {
		return ErrUnsafe
	}
	return nil
}

func inventory(root string, uid uint32, immutable bool, exclude ...string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return ErrUnsafe
		}
		if entry.IsDir() {
			fd, e := openDirectory(path, uid, immutable)
			if e != nil {
				return e
			}
			unixClose(fd)
			return nil
		}
		for _, skip := range exclude {
			if path == skip {
				return nil
			}
		}
		if len(files) >= 4096 {
			return errors.New("ERP_RUNTIME_RECEIPT_LIMIT")
		}
		f, e := OpenFile(path, uid, 0, immutable)
		if e != nil {
			return e
		}
		hash, e := FileDigest(f)
		f.Close()
		if e != nil {
			return e
		}
		files[path] = hash
		return nil
	})
	return files, err
}
func copyTree(source, target string, uid uint32) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return ErrUnsafe
		}
		relative, e := filepath.Rel(source, path)
		if e != nil {
			return ErrUnsafe
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0755)
		}
		f, e := OpenFile(path, uid, 0, false)
		if e != nil {
			return e
		}
		defer f.Close()
		raw, e := io.ReadAll(io.LimitReader(f, 128<<20+1))
		if e != nil || len(raw) > 128<<20 {
			return ErrInput
		}
		st, e := f.Stat()
		if e != nil {
			return ErrUnsafe
		}
		return WriteExclusive(destination, raw, uint32(st.Mode().Perm()))
	})
}

// This fixed probe imports only the delivered incremental closure, then lists
// all stdlib/site-packages data/native files and actual mapped base libraries.
const runtimeProbe = `import json, os, sys, sysconfig
import bi_agent.yanfa_bridge, bi_agent.sync
paths=set()
for root in {sysconfig.get_path('stdlib'),sysconfig.get_path('platstdlib'),sysconfig.get_path('purelib'),sysconfig.get_path('platlib')}:
 for parent, dirs, names in os.walk(root, followlinks=False):
  dirs[:]=[d for d in dirs if d not in ('__pycache__','site-packages') or os.path.join(parent,d) in (sysconfig.get_path('purelib'),sysconfig.get_path('platlib'))]
  for name in names:
   if not name.endswith(('.pyc','.pyo')): paths.add(os.path.join(parent,name))
for line in open('/proc/self/maps'):
 fields=line.split()
 if len(fields)>=6 and fields[-1].startswith('/'): paths.add(os.path.realpath(fields[-1]))
paths.add(sys.executable)
paths.add(os.path.join(sys.prefix,'pyvenv.cfg'))
assert not os.path.exists(os.path.join(sys.prefix,'bin','bi-inventory-monitor'))
assert not os.path.exists(os.path.join(sys.prefix,'bin','bi-inventory-outbox'))
print(json.dumps(sorted(paths)))
`

func Install(ctx context.Context, req Request) (*config.ERPInstallationReceipt, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, errors.New("TRUSTED_INSTALL_POSITIVE_BLOCKED")
	}
	var prepared Preparation
	if ReadJSON(filepath.Join(req.StagingDirectory, "preparation.json"), &prepared) != nil {
		return nil, ErrInput
	}
	requestRaw, _ := json.Marshal(req)
	if prepared.RequestSHA256 != Digest(requestRaw) {
		return nil, ErrInput
	}
	for path, hash := range prepared.PreparedFiles {
		if !strings.HasPrefix(path, req.StagingDirectory+"/") {
			return nil, ErrUnsafe
		}
		f, e := OpenFile(path, 0, 0, true)
		if e != nil {
			return nil, e
		}
		actual, e := FileDigest(f)
		f.Close()
		if e != nil || actual != hash {
			return nil, ErrInput
		}
	}
	if fd, err := openDirectory(filepath.Dir(req.ReleaseDirectory), 0, true); err != nil {
		return nil, err
	} else {
		unixClose(fd)
	}
	if os.Mkdir(req.ReleaseDirectory, 0755) != nil {
		return nil, ErrUnsafe
	}
	if err := copyTree(filepath.Join(req.StagingDirectory, "python"), filepath.Join(req.ReleaseDirectory, "python"), 0); err != nil {
		return nil, err
	}
	if err := copyTree(filepath.Join(req.StagingDirectory, "source/dependency-recipe"), filepath.Join(req.ReleaseDirectory, "dependency-recipe"), 0); err != nil {
		return nil, err
	}
	preparationRaw, e := ReadFile(filepath.Join(req.StagingDirectory, "preparation.json"), 0, 1<<20)
	if e != nil {
		return nil, e
	}
	if WriteExclusive(filepath.Join(req.ReleaseDirectory, "dependency-recipe/preparation.json"), preparationRaw, 0644) != nil {
		return nil, ErrUnsafe
	}
	raw, err := ReadFile(req.ArtifactPath, 0, 32<<20)
	if err != nil || Digest(raw) != req.ArtifactSHA256 {
		return nil, ErrInput
	}
	artifact := filepath.Join(req.ReleaseDirectory, "source.tar.gz")
	if WriteExclusive(artifact, raw, 0644) != nil {
		return nil, ErrUnsafe
	}
	python := req.Manifest.ExecutableAbsolutePath
	probe, err := run(ctx, python, req.ReleaseDirectory, "-I", "-B", "-c", runtimeProbe)
	if err != nil {
		return nil, err
	}
	var paths []string
	if DecodeJSON(probe, &paths) != nil {
		return nil, ErrInput
	}
	files, err := inventory(req.ReleaseDirectory, 0, true)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if _, ok := files[path]; ok {
			continue
		}
		f, e := OpenFile(path, 0, 0, true)
		if e != nil {
			return nil, e
		}
		hash, e := FileDigest(f)
		f.Close()
		if e != nil {
			return nil, e
		}
		files[path] = hash
	}
	if len(files) > 4096 {
		return nil, errors.New("ERP_RUNTIME_RECEIPT_LIMIT")
	}
	manifest := req.Manifest
	if manifest.MaintenanceOperations == nil {
		manifest.MaintenanceOperations = []string{}
	}
	manifestRaw, _ := json.MarshalIndent(manifest, "", "  ")
	manifestRaw = append(manifestRaw, '\n')
	manifestPath := filepath.Join(req.ReleaseDirectory, "manifest.json")
	if WriteExclusive(manifestPath, manifestRaw, 0644) != nil {
		return nil, ErrUnsafe
	}
	mnt, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return nil, ErrUnsafe
	}
	pid, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return nil, ErrUnsafe
	}
	receipt := &config.ERPInstallationReceipt{ManifestSHA256: Digest(manifestRaw), ArtifactPath: artifact, LockPath: filepath.Join(req.ReleaseDirectory, "dependency-recipe/uv.lock"), ExecutableSHA256: files[python], InstalledFiles: files, PythonVersion: req.PythonVersion, UVVersion: UVVersion, InstallRecipe: Recipe, RuntimeMountNamespace: mnt, RuntimePIDNamespace: pid, ApprovedBy: req.Manifest.ApprovedBy, ApprovedAt: req.Manifest.ApprovedAt}
	receiptRaw, _ := json.MarshalIndent(receipt, "", "  ")
	receiptRaw = append(receiptRaw, '\n')
	receiptPath := filepath.Join(req.ReleaseDirectory, "installation.json")
	if WriteExclusive(receiptPath, receiptRaw, 0644) != nil {
		return nil, ErrUnsafe
	}
	if err := Verify(VerificationRequest{ManifestPath: manifestPath, ManifestSHA256: Digest(manifestRaw), ReceiptPath: receiptPath, ReceiptSHA256: Digest(receiptRaw), DeploymentUID: req.Manifest.DeploymentUID}); err != nil {
		return nil, err
	}
	return receipt, nil
}
func Verify(req VerificationRequest) error {
	read := func(path, hash string, target interface{}) error {
		f, e := OpenFile(path, 0, 0, true)
		if e != nil {
			return e
		}
		defer f.Close()
		raw, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
		if e != nil || len(raw) > 1<<20 || Digest(raw) != hash {
			return ErrInput
		}
		return DecodeJSON(raw, target)
	}
	var manifest config.ERPComponentManifest
	var receipt config.ERPInstallationReceipt
	if read(req.ManifestPath, req.ManifestSHA256, &manifest) != nil || read(req.ReceiptPath, req.ReceiptSHA256, &receipt) != nil {
		return ErrUnsafe
	}
	if manifest.BridgeProtocol != "yanfa-erp-bridge/1" || manifest.DeploymentUID != req.DeploymentUID || manifest.UVVersion != UVVersion || manifest.InstallRecipe != Recipe || manifest.ApprovedBy == "" || receipt.ApprovedBy == "" || receipt.ManifestSHA256 != req.ManifestSHA256 || receipt.PythonVersion != manifest.PythonVersion || receipt.UVVersion != manifest.UVVersion || receipt.InstallRecipe != manifest.InstallRecipe || len(receipt.InstalledFiles) == 0 || len(receipt.InstalledFiles) > 4096 || receipt.InstalledFiles[manifest.ExecutableAbsolutePath] != receipt.ExecutableSHA256 {
		return ErrInput
	}
	mnt, e := os.Readlink("/proc/self/ns/mnt")
	if e != nil || mnt != receipt.RuntimeMountNamespace {
		return ErrUnsafe
	}
	pid, e := os.Readlink("/proc/self/ns/pid")
	if e != nil || pid != receipt.RuntimePIDNamespace {
		return ErrUnsafe
	}
	files := make(map[string]string, len(receipt.InstalledFiles)+2)
	for path, hash := range receipt.InstalledFiles {
		files[path] = hash
	}
	files[receipt.ArtifactPath] = manifest.ArtifactSHA256
	files[receipt.LockPath] = manifest.LockSHA256
	bridge, venv, native := false, false, false
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		f, e := OpenFile(path, 0, 0, true)
		if e != nil {
			return e
		}
		hash, e := FileDigest(f)
		f.Close()
		if e != nil || hash != files[path] {
			return ErrInput
		}
		bridge = bridge || strings.HasSuffix(path, "/bi_agent/yanfa_bridge.py")
		venv = venv || strings.HasSuffix(path, "/pyvenv.cfg")
		native = native || strings.Contains(path, ".so")
	}
	if !bridge || !venv || !native || checkedExecutable(manifest.ExecutableAbsolutePath, receipt.ExecutableSHA256, 0) != nil {
		return ErrInput
	}
	if len(manifest.MaintenanceOperations) > 1 || len(manifest.MaintenanceOperations) == 1 && manifest.MaintenanceOperations[0] != "incremental" {
		return ErrInput
	}
	if _, err := time.Parse(time.RFC3339, manifest.ApprovedAt); err != nil {
		return ErrInput
	}
	if _, err := time.Parse(time.RFC3339, receipt.ApprovedAt); err != nil {
		return ErrInput
	}
	if !strings.HasPrefix(manifest.WorkingDirectory, "/opt/yanfa/erp-sync/releases/") || filepath.Dir(manifest.WorkingDirectory) != "/opt/yanfa/erp-sync/releases" || manifest.ExecutableAbsolutePath != manifest.WorkingDirectory+"/python/bin/python" {
		return ErrInput
	}
	actual, err := inventory(manifest.WorkingDirectory, 0, true, req.ManifestPath, req.ReceiptPath)
	if err != nil {
		return err
	}
	for path, hash := range actual {
		if receipt.InstalledFiles[path] != hash {
			return ErrInput
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe, err := run(ctx, manifest.ExecutableAbsolutePath, manifest.WorkingDirectory, "-I", "-B", "-c", runtimeProbe)
	if err != nil {
		return err
	}
	var required []string
	if DecodeJSON(probe, &required) != nil {
		return ErrInput
	}
	for _, path := range required {
		if receipt.InstalledFiles[path] == "" {
			return ErrInput
		}
	}
	return nil
}
