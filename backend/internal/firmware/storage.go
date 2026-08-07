package firmware

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Storage is local-disk firmware file storage — see the package doc for
// why this stands in for S3/MinIO in this build. One file per image,
// named by its Repository ID so the DB row and the on-disk file are
// always found the same way.
type Storage struct {
	root string
}

func NewStorage(root string) (*Storage, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create firmware storage root: %w", err)
	}
	return &Storage{root: root}, nil
}

// Save streams r to disk under id, returning the SHA256 and byte count
// computed while writing — never buffers the whole image in memory.
func (s *Storage) Save(id string, r io.Reader) (sha256hex string, size int64, err error) {
	path := s.path(id)
	f, err := os.Create(path)
	if err != nil {
		return "", 0, fmt.Errorf("create firmware file: %w", err)
	}
	defer f.Close()

	hr := NewHashReader(r)
	if _, err := io.Copy(f, hr); err != nil {
		os.Remove(path)
		return "", 0, fmt.Errorf("write firmware file: %w", err)
	}
	return hr.Sum(), hr.BytesRead(), nil
}

// Open returns the stored file for reading (serving it over HTTP).
func (s *Storage) Open(id string) (*os.File, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return nil, fmt.Errorf("open firmware file: %w", err)
	}
	return f, nil
}

func (s *Storage) path(id string) string {
	return filepath.Join(s.root, id+".bin")
}
