// Package storage owns the S3-compatible object store for large secret
// payloads. The server sees only ciphertext and safe metadata.
package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Config configures the S3-compatible object store. Endpoint/PathStyle target
// MinIO (dev) and OCI S3-compat (prod); an empty Endpoint uses the AWS default
// host. Auth is always a static key pair (OCI Customer Secret Key in prod).
// Instance principal is deferred because the AWS SDK cannot speak it directly.
type Config struct {
	Enabled         bool
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
}

// POSTForm is a presigned POST upload instruction returned to the client. The
// browser posts multipart/form-data with Fields (any order) followed by the
// file part named FileField, to URL.
type POSTForm struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	ExpiresAt time.Time         `json:"expires_at"`
	Fields    map[string]string `json:"fields"`
	FileField string            `json:"file_field"`
}

// ObjectInfo describes an object. Exists is false for a 404.
type ObjectInfo struct {
	Key         string
	Exists      bool
	Size        int64
	ContentType string
}

// ObjectStore is the surface the secrets store depends on for large payloads.
type ObjectStore interface {
	PresignPOST(ctx context.Context, key string, maxSize int64, ttl time.Duration) (POSTForm, error)
	Head(ctx context.Context, key string) (ObjectInfo, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

type Client struct {
	cfg Config
	s3  *s3.Client
	now func() time.Time
}

// New builds an S3 client. Returns an error when disabled so callers can treat
// object storage as optional wiring.
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

// SetNowForTest injects a deterministic clock for presigned policy timestamps.
func (c *Client) SetNowForTest(now func() time.Time) { c.now = now }

// postURL is the POST action target (bucket root).
func (c *Client) postURL() string {
	if c.cfg.Endpoint != "" {
		return strings.TrimRight(c.cfg.Endpoint, "/") + "/" + c.cfg.Bucket
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/", c.cfg.Bucket, c.cfg.Region)
}

var _ ObjectStore = (*Client)(nil)
