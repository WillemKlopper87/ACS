package firmware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestHashReaderComputesSHA256WhileReading(t *testing.T) {
	data := []byte("firmware image bytes, not actually a real firmware blob")
	want := sha256.Sum256(data)

	hr := NewHashReader(bytes.NewReader(data))
	buf := make([]byte, 8) // small buffer to force multiple Read calls
	var total int64
	for {
		n, rerr := hr.Read(buf)
		total += int64(n)
		if rerr != nil {
			break
		}
	}

	if hr.Sum() != hex.EncodeToString(want[:]) {
		t.Errorf("Sum() = %q, want %q", hr.Sum(), hex.EncodeToString(want[:]))
	}
	if hr.BytesRead() != int64(len(data)) {
		t.Errorf("BytesRead() = %d, want %d", hr.BytesRead(), len(data))
	}
	if total != int64(len(data)) {
		t.Errorf("total bytes read via Read() = %d, want %d", total, len(data))
	}
}

func TestStorageSaveAndOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	storage, err := NewStorage(dir)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}

	content := []byte("a fake firmware image, just bytes for the round trip test")
	sha, size, err := storage.Save("image-1", bytes.NewReader(content))
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

	f, err := storage.Open("image-1")
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
	} else if !strings.Contains(err.Error(), "open object file") {
		t.Errorf("unexpected error shape: %v", err)
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
