package storage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// PresignPUT returns a presigned PUT whose signature covers Content-Length, so
// the bucket rejects any body that is not exactly size bytes — an upload of the
// wrong length fails authentication before the object lands. That is stricter
// than the content-length-range this replaced, which allowed anything up to a
// ceiling.
//
// PUT rather than POST because Oracle's S3 Compatibility API does not implement
// POST Object (browser form upload with a policy document); a presigned POST
// there returns 403 SignatureDoesNotMatch. PUT is supported by every
// S3-compatible store this targets, MinIO included.
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
		return UploadInstruction{}, fmt.Errorf("presign put %q: %w", key, err)
	}

	// Content-Length is in the signature, so the browser has to send exactly
	// this value. fetch() sets it from the body, which is why the client must
	// upload the raw ciphertext rather than a multipart envelope.
	return UploadInstruction{
		URL:       req.URL,
		Method:    req.Method,
		ExpiresAt: c.now().UTC().Add(ttl),
		Headers:   map[string]string{"Content-Length": strconv.FormatInt(size, 10)},
	}, nil
}
