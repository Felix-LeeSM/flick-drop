// Package storage: the server sees only ciphertext and safe metadata.
package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Auth is a static key pair only; the AWS SDK cannot speak OCI instance principal.
type Config struct {
	Enabled         bool
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
}

// Headers are part of the signature; the browser must echo them verbatim (notably
// Content-Length) or the object store rejects the upload.
type UploadInstruction struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	ExpiresAt time.Time         `json:"expires_at"`
	Headers   map[string]string `json:"headers"`
}

type ObjectInfo struct {
	Key         string
	Exists      bool
	Size        int64
	ContentType string
}

type ObjectStore interface {
	List(ctx context.Context, prefix, cursor string, limit int) (ObjectPage, error)
	PresignPUT(ctx context.Context, key string, size int64, ttl time.Duration) (UploadInstruction, error)
	Head(ctx context.Context, key string) (ObjectInfo, error)
	// The caller must close the body.
	Open(ctx context.Context, key string) (io.ReadCloser, int64, error)
	Delete(ctx context.Context, key string) error
}

type RequestObjectStore interface {
	ObjectStore
	// verify runs after all size bytes are read and before the write.
	PutStream(ctx context.Context, key string, body io.Reader, size int64, verify func() error) error
}

type Client struct {
	cfg Config
	s3  *s3.Client
	now func() time.Time
}

func New(cfg Config) (*Client, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("storage backend is disabled")
	}
	if cfg.Bucket == "" || cfg.Region == "" {
		return nil, fmt.Errorf("bucket and region are required")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("access key id and secret access key are required")
	}

	var baseEndpoint *string
	if cfg.Endpoint != "" {
		baseEndpoint = aws.String(cfg.Endpoint)
	}
	s3Client := s3.New(s3.Options{
		Region:       cfg.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		BaseEndpoint: baseEndpoint,
		UsePathStyle: cfg.PathStyle,
	})

	return &Client{
		cfg: cfg,
		s3:  s3Client,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (c *Client) SetNowForTest(now func() time.Time) { c.now = now }

var _ ObjectStore = (*Client)(nil)
var _ RequestObjectStore = (*Client)(nil)
