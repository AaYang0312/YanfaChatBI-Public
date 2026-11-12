package erpsyncinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var ErrUnsafe = errors.New("ERP_INSTALL_UNSAFE_PATH")
var ErrInput = errors.New("ERP_INSTALL_INPUT_INVALID")

func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func FileDigest(file *os.File) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", ErrInput
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func openDirectory(path string, uid uint32, immutable bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, ErrUnsafe
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrUnsafe
	}
	if path == "/" {
		return fd, nil
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return -1, ErrUnsafe
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&0022 != 0 || st.Uid != 0 && (immutable || st.Uid != uid) {
			unix.Close(fd)
			return -1, ErrUnsafe
		}
	}
	return fd, nil
}
func OpenFile(path string, uid, mode uint32, immutable bool) (*os.File, error) {
	if filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	dir, err := openDirectory(filepath.Dir(path), uid, immutable)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnsafe
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uid || st.Nlink != 1 || st.Mode&0022 != 0 || mode != 0 && st.Mode&07777 != mode {
		unix.Close(fd)
		return nil, ErrUnsafe
	}
	return os.NewFile(uintptr(fd), "erp-approved-input"), nil
}
func ReadFile(path string, uid uint32, max int64) ([]byte, error) {
	f, err := OpenFile(path, uid, 0, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, ErrInput
	}
	return raw, nil
}

// Public deployment metadata may be held by root or the approved UID. Its
// private parent chain remains root/UID-owned and never group/world writable.
func ReadPublicFile(path string, uid uint32, max int64) ([]byte, error) {
	if filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	dir, err := openDirectory(filepath.Dir(path), uid, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "erp-public-config")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Mode&0022 != 0 || st.Uid != 0 && st.Uid != uid {
		return nil, ErrUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, ErrInput
	}
	return raw, nil
}

// DecodeJSON rejects duplicate keys recursively, unknown struct fields, case
// aliases and trailing input. Values never appear in an error.
func DecodeJSON(raw []byte, target interface{}) error {
	if !utf8.Valid(raw) {
		return ErrInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	depth := 0
	walk = func() error {
		depth++
		defer func() { depth-- }()
		if depth > 16 {
			return ErrInput
		}
		t, err := d.Token()
		if err != nil {
			return ErrInput
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				t, e := d.Token()
				key, ok := t.(string)
				if e != nil || !ok || seen[key] {
					return ErrInput
				}
				seen[key] = true
				if walk() != nil {
					return ErrInput
				}
			}
			t, e := d.Token()
			if e != nil || t != json.Delim('}') {
				return ErrInput
			}
		case '[':
			for d.More() {
				if walk() != nil {
					return ErrInput
				}
			}
			t, e := d.Token()
			if e != nil || t != json.Delim(']') {
				return ErrInput
			}
		default:
			return ErrInput
		}
		return nil
	}
	if walk() != nil || d.Decode(new(interface{})) != io.EOF {
		return ErrInput
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return ErrInput
	}
	// Compare the structural keys against the actual marshaled field names to
	// prevent encoding/json's case-insensitive struct fallback.
	canonical, err := json.Marshal(target)
	if err != nil {
		return ErrInput
	}
	var supplied, decoded interface{}
	if json.Unmarshal(raw, &supplied) != nil || json.Unmarshal(canonical, &decoded) != nil {
		return ErrInput
	}
	var keys func(interface{}, interface{}) bool
	keys = func(a, b interface{}) bool {
		switch x := a.(type) {
		case map[string]interface{}:
			y, ok := b.(map[string]interface{})
			if !ok {
				return false
			}
			for k, v := range x {
				w, ok := y[k]
				if !ok || !keys(v, w) {
					return false
				}
			}
		case []interface{}:
			y, ok := b.([]interface{})
			if !ok || len(x) != len(y) {
				return false
			}
			for i := range x {
				if !keys(x[i], y[i]) {
					return false
				}
			}
		}
		return true
	}
	if !keys(supplied, decoded) {
		return ErrInput
	}
	return nil
}
func ReadJSON(path string, target interface{}) error {
	raw, err := ReadFile(path, uint32(os.Geteuid()), 1<<20)
	if err != nil {
		return err
	}
	return DecodeJSON(raw, target)
}
func WriteExclusive(path string, raw []byte, mode uint32) error {
	dir, err := openDirectory(filepath.Dir(path), uint32(os.Geteuid()), false)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, filepath.Base(path), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "erp-new-output")
	defer f.Close()
	if f.Chmod(os.FileMode(mode)) != nil {
		return ErrUnsafe
	}
	if _, err := f.Write(raw); err != nil {
		return ErrInput
	}
	if f.Sync() != nil || unix.Fsync(dir) != nil {
		return ErrInput
	}
	return nil
}
