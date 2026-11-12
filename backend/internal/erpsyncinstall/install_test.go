package erpsyncinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.MkdirTemp(home, ".t40-install-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(path) })
	return path
}
func TestERPInstallLiteralConversionAndNoOverwrite(t *testing.T) {
	directory := privateDirectory(t)
	source, target := filepath.Join(directory, "source.env"), filepath.Join(directory, "private.json")
	raw := []byte("BI_WRITER_DSN='postgresql://synthetic:literal value@127.0.0.1/bi_agent'\nBI_SHOP_IDS=1,2,3,4,5,6,7\nKUAI_MAI_APP_KEY=canary\nKUAI_MAI_APP_SECRET='literal $not_expanded'\nKUAI_MAI_ACCESS_TOKEN=access\nKUAI_MAI_REFRESH_TOKEN=refresh\n")
	if os.WriteFile(source, raw, 0600) != nil {
		t.Fatal("fixture")
	}
	req := ConvertRequest{Source: source, Destination: target, UID: uint32(os.Geteuid()), AuthorizationRef: "synthetic-only"}
	if err := ConvertConfig(req); err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if ReadJSON(target, &values) != nil || values["KUAI_MAI_APP_SECRET"] != "literal $not_expanded" {
		t.Fatal("literal changed")
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if ConvertConfig(req) == nil {
		t.Fatal("target overwritten")
	}
	after, _ := os.ReadFile(target)
	if !bytes.Equal(before, after) {
		t.Fatal("existing target changed")
	}
	for _, bad := range [][]byte{append(append([]byte(nil), raw...), []byte("BI_SHOP_IDS=8\n")...), bytes.Replace(raw, []byte("KUAI_MAI_APP_KEY=canary"), []byte("KUAI_MAI_APP_KEY=$(touch /tmp/t40-canary)"), 1), append(append([]byte(nil), raw...), []byte("export UNKNOWN=x\n")...), bytes.Replace(raw, []byte("KUAI_MAI_ACCESS_TOKEN=access"), []byte("KUAI_MAI_ACCESS_TOKEN=\"$secret\""), 1)} {
		if _, err := ParseDotenv(bad); !errors.Is(err, ErrConfig) {
			t.Fatal("shell/duplicate syntax accepted")
		}
	}
	link := filepath.Join(directory, "link.env")
	if os.Symlink(source, link) != nil {
		t.Fatal("symlink fixture")
	}
	req.Source, req.Destination = link, filepath.Join(directory, "other.json")
	if ConvertConfig(req) == nil {
		t.Fatal("private symlink followed")
	}
}
func TestERPInstallOriginalWheelRequired(t *testing.T) {
	directory := privateDirectory(t)
	requirements := []byte("example==1.0 --hash=sha256:" + strings.Repeat("a", 64) + "\n")
	if _, err := wheelInputs(requirements, directory, uint32(os.Geteuid())); err == nil || err.Error() != "LOCKED_WHEEL_MISSING:example==1.0" {
		t.Fatal("missing wheel was accepted")
	}
	// Installed module/RECORD bytes do not constitute a locked wheel input.
	if os.WriteFile(filepath.Join(directory, "RECORD"), []byte("copied module"), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := wheelInputs(requirements, directory, uint32(os.Geteuid())); err == nil {
		t.Fatal("RECORD substituted for wheel")
	}
	wheel := filepath.Join(directory, "example-1.0-py3-none-any.whl")
	if os.WriteFile(wheel, []byte("wrong archive bytes"), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := wheelInputs(requirements, directory, uint32(os.Geteuid())); err == nil || err.Error() != "LOCKED_WHEEL_HASH_MISMATCH" {
		t.Fatal("unlocked wheel accepted")
	}
	conditional := []byte("psycopg-binary==3.3.5 ; implementation_name != 'pypy' --hash=sha256:" + strings.Repeat("b", 64) + "\n")
	if _, err := wheelInputs(conditional, directory, uint32(os.Geteuid())); err == nil || err.Error() != "LOCKED_WHEEL_MISSING:psycopg-binary==3.3.5" {
		t.Fatal("original locked marker did not retain its binary wheel requirement")
	}
}
func TestERPInstallUnsafeArchiveAndTrust(t *testing.T) {
	directory := privateDirectory(t)
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	archive := tar.NewWriter(gz)
	archive.WriteHeader(&tar.Header{Name: "../escaped", Typeflag: tar.TypeReg, Mode: 0644, Size: 1})
	archive.Write([]byte("x"))
	archive.Close()
	gz.Close()
	if err := extractArtifact(raw.Bytes(), directory); err == nil {
		t.Fatal("archive escaped staging")
	}
	file := filepath.Join(directory, "python")
	if os.WriteFile(file, []byte("untrusted candidate"), 0700) != nil {
		t.Fatal("fixture")
	}
	if _, err := OpenFile(file, 0, 0, true); err == nil {
		t.Fatal("writable user tree trusted as root installation")
	}
	if os.Chmod(directory, 0770) != nil {
		t.Fatal("fixture")
	}
	if _, err := OpenFile(file, uint32(os.Geteuid()), 0, false); err == nil {
		t.Fatal("writable ancestor accepted")
	}
}
func TestERPInstallStrictJSON(t *testing.T) {
	var req ConvertRequest
	for _, raw := range []string{`{"uid":1,"uid":2}`, `{"UID":1}`, `{"uid":1} {"uid":2}`} {
		if DecodeJSON([]byte(raw), &req) == nil {
			t.Fatal("ambiguous configuration accepted")
		}
	}
}

func TestERPInstallRemovesOnlyUVEnvironmentLock(t *testing.T) {
	dir := privateDirectory(t)
	uid := uint32(os.Geteuid())
	lock := filepath.Join(dir, ".lock")
	if err := removeUVEnvironmentLock(dir, uid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lock, 0666); err != nil {
		t.Fatal(err)
	}
	if err := removeUVEnvironmentLock(dir, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty uv lock was left in the prepared environment")
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lock); err != nil {
		t.Fatal(err)
	}
	if err := removeUVEnvironmentLock(dir, uid); !errors.Is(err, ErrUnsafe) {
		t.Fatal("symlink was accepted as a temporary uv lock")
	}
	if _, err := os.Lstat(lock); err != nil {
		t.Fatal("unexpected path was removed")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte("not a transient lock"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeUVEnvironmentLock(dir, uid); !errors.Is(err, ErrUnsafe) {
		t.Fatal("nonempty file was accepted as a temporary uv lock")
	}
}
