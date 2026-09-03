// Package objstore abstracts where firmware images and CPE-uploaded
// files live (audit P2.3: "move binaries to S3-compatible object
// storage"). Two backends: local disk (the historical default, one
// process, one host) and S3-compatible (AWS S3, MinIO, Ceph RGW, …),
// which is what lets several API replicas serve the same firmware and
// receive uploads without a shared filesystem.
package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Store is one object namespace (firmware or uploads).
type Store interface {
	// Save streams r into the object named id, returning its SHA-256
	// and byte count computed while writing.
	Save(id string, r io.Reader) (sha256hex string, size int64, err error)
	// Open returns the object for reading and seeking (http.ServeContent
	// needs Range support). Close releases any local staging.
	Open(id string) (io.ReadSeekCloser, error)
	// Remove deletes the object; a missing object is not an error.
	Remove(id string)
	// Rename moves the object named oldID to newID (audit P1.3/M-13):
	// callers that race two concurrent writers for the same logical slot
	// save each writer to its own unique, never-colliding id and rename
	// only the one an atomic DB check declares the winner into the
	// shared final id — the loser's Remove(oldID) then only ever touches
	// its own never-promoted object, not whatever the winner wrote.
	Rename(oldID, newID string) error
}

// hashReader tees r into a SHA-256 while counting bytes.
type hashReader struct {
	r    io.Reader
	h    hash.Hash
	size int64
}

func newHashReader(r io.Reader) *hashReader {
	h := sha256.New()
	return &hashReader{r: io.TeeReader(r, h), h: h}
}

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.size += int64(n)
	return n, err
}

func (h *hashReader) sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

// FromEnv builds the Store selected by ACS_OBJECT_STORE ("local", the
// default, or "s3"). localRoot is the on-disk directory for the local
// backend; prefix is the key prefix under the bucket for the S3 one
// (both stores share one bucket, e.g. "firmware/" and "uploads/").
//
// S3 settings: ACS_S3_BUCKET (required), ACS_S3_REGION, ACS_S3_ENDPOINT
// (MinIO/other S3-compatible endpoints), ACS_S3_PATH_STYLE=true for
// endpoints without virtual-host bucket DNS. Credentials come from the
// standard AWS chain (env, shared config, instance role).
func FromEnv(logger *slog.Logger, localRoot, prefix string) (Store, error) {
	switch kind := strings.ToLower(os.Getenv("ACS_OBJECT_STORE")); kind {
	case "", "local":
		return NewLocal(localRoot)
	case "s3":
		bucket := os.Getenv("ACS_S3_BUCKET")
		if bucket == "" {
			return nil, fmt.Errorf("ACS_OBJECT_STORE=s3 requires ACS_S3_BUCKET")
		}
		st, err := NewS3(S3Config{
			Bucket:    bucket,
			Prefix:    prefix,
			Region:    os.Getenv("ACS_S3_REGION"),
			Endpoint:  os.Getenv("ACS_S3_ENDPOINT"),
			PathStyle: os.Getenv("ACS_S3_PATH_STYLE") == "true",
		})
		if err != nil {
			return nil, err
		}
		logger.Info("object store: s3", "bucket", bucket, "prefix", prefix, "endpoint", os.Getenv("ACS_S3_ENDPOINT"))
		return st, nil
	default:
		return nil, fmt.Errorf("unknown ACS_OBJECT_STORE %q (want local or s3)", kind)
	}
}
