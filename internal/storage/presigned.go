package storage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Content-Length is signed so the bucket rejects any other size before the object lands.
// PUT, not POST: OCI's S3 Compatibility API lacks POST Object (403 SignatureDoesNotMatch).
func (c *Client) PresignPUT(ctx context.Context, key string, size int64, ttl time.Duration) (UploadInstruction, error) {
	if key == "" {
		return UploadInstruction{}, fmt.Errorf("object key is required")
	}
	if size <= 0 {
		return UploadInstruction{}, fmt.Errorf("size must be positive")
	}

	req, err := s3.NewPresignClient(c.s3).PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.cfg.Bucket),
		Key:           aws.String(key),
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return UploadInstruction{}, fmt.Errorf("presign put: %w", err)
	}

	// fetch() sets Content-Length from the body, so the client must upload raw ciphertext, not multipart.
	return UploadInstruction{
		URL:       req.URL,
		Method:    req.Method,
		ExpiresAt: c.now().UTC().Add(ttl),
		Headers:   map[string]string{"Content-Length": strconv.FormatInt(size, 10)},
	}, nil
}
