package objstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
)

func TestLocalRoundTrip(t *testing.T) {
	st, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("firmware!"), 1000)
	sum, size, err := st.Save("img-1", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(payload)
	if sum != hex.EncodeToString(want[:]) || size != int64(len(payload)) {
		t.Errorf("Save = (%s, %d), want (%x, %d)", sum, size, want, len(payload))
	}
	f, err := st.Open("img-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(9, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, payload[9:]) {
		t.Error("seek+read returned wrong bytes")
	}
	f.Close() // Windows refuses to delete a file with an open handle
	st.Remove("img-1")
	if _, err := st.Open("img-1"); err == nil {
		t.Error("Open after Remove succeeded")
	}
}

func TestLocalIDCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	st, _ := NewLocal(root)
	if _, _, err := st.Save("../../escape", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root + "/escape.bin"); err != nil {
		t.Errorf("object was not confined to root: %v", err)
	}
}

func TestFromEnvRejectsUnknownKind(t *testing.T) {
	t.Setenv("ACS_OBJECT_STORE", "ftp")
	if _, err := FromEnv(nil, t.TempDir(), "x/"); err == nil {
		t.Error("unknown backend accepted")
	}
	t.Setenv("ACS_OBJECT_STORE", "s3")
	t.Setenv("ACS_S3_BUCKET", "")
	if _, err := FromEnv(nil, t.TempDir(), "x/"); err == nil {
		t.Error("s3 without bucket accepted")
	}
}
