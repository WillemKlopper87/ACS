package uploads

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestStorageSaveAndOpenRoundTrip(t *testing.T) {
	storage, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}

	content := []byte("a fake vendor config backup file, just bytes for the round trip test")
	sha, size, err := storage.Save("upload-1", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	want := sha256.Sum256(content)
	if sha != hex.EncodeToString(want[:]) {
		t.Errorf("Save sha256 = %q, want %q", sha, hex.EncodeToString(want[:]))
	}
	if size != int64(len(content)) {
		t.Errorf("Save size = %d, want %d", size, len(content))
	}

	f, err := storage.Open("upload-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Error("round-tripped content did not match what was saved")
	}
}

func TestStorageOpenMissingFile(t *testing.T) {
	storage, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	if _, err := storage.Open("does-not-exist"); err == nil {
		t.Error("Open of a never-saved id should error, not silently return a handle")
	}
}

func TestNewStorageCreatesRootIfMissing(t *testing.T) {
	dir := t.TempDir() + "/nested/does/not/exist/yet"
	if _, err := NewStorage(dir); err != nil {
		t.Fatalf("NewStorage should create the root directory tree: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("expected %s to exist as a directory after NewStorage", dir)
	}
}
