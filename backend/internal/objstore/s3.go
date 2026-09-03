package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config selects the bucket and, for S3-compatible services, the
// endpoint and addressing style.
type S3Config struct {
	Bucket    string
	Prefix    string
	Region    string
	Endpoint  string
	PathStyle bool
}

// S3 is the S3-compatible backend. Uploads stream through the SDK's
// multipart uploader (no whole-object buffering); reads stage the
// object into a temp file so http.ServeContent gets a seekable body and
// Range requests keep working exactly as with local disk.
type S3 struct {
	client   *s3.Client
	uploader *manager.Uploader
	bucket   string
	prefix   string
}

func NewS3(cfg S3Config) (*S3, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.PathStyle
	})
	return &S3{
		client:   client,
		uploader: manager.NewUploader(client),
		bucket:   cfg.Bucket,
		prefix:   cfg.Prefix,
	}, nil
}

func (s *S3) key(id string) string { return s.prefix + id + ".bin" }

func (s *S3) Save(id string, r io.Reader) (string, int64, error) {
	hr := newHashReader(r)
	_, err := s.uploader.Upload(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(id)),
		Body:   hr,
	})
	if err != nil {
		return "", 0, fmt.Errorf("s3 upload %s: %w", s.key(id), err)
	}
	return hr.sum(), hr.size, nil
}

// stagedObject is a temp file that deletes itself on Close.
type stagedObject struct {
	*os.File
}

func (o *stagedObject) Close() error {
	name := o.File.Name()
	err := o.File.Close()
	os.Remove(name)
	return err
}

func (s *S3) Open(id string) (io.ReadSeekCloser, error) {
	out, err := s.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(id)),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", s.key(id), err)
	}
	defer out.Body.Close()
	tmp, err := os.CreateTemp("", "acs-obj-*")
	if err != nil {
		return nil, fmt.Errorf("stage s3 object: %w", err)
	}
	if _, err := io.Copy(tmp, out.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("stage s3 object %s: %w", s.key(id), err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	return &stagedObject{File: tmp}, nil
}

func (s *S3) Remove(id string) {
	_, _ = s.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(id)),
	})
}

// Rename copies oldID's object onto newID's key and deletes the original
// — S3 has no atomic rename, but CopyObject+DeleteObject is the standard
// substitute and is only ever called once per upload (the race winner),
// not on the hot path.
func (s *S3) Rename(oldID, newID string) error {
	source := s.bucket + "/" + s.key(oldID)
	if _, err := s.client.CopyObject(context.Background(), &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(s.key(newID)),
		CopySource: aws.String(source),
	}); err != nil {
		return fmt.Errorf("s3 copy %s to %s: %w", source, s.key(newID), err)
	}
	s.Remove(oldID)
	return nil
}

// ErrNotConfigured is returned by FromEnv-adjacent helpers when the S3
// backend is selected without a bucket; kept exported for callers that
// want to special-case it.
var ErrNotConfigured = errors.New("object store not configured")
