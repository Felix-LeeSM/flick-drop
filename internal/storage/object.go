package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ErrObjectTooLarge = errors.New("object exceeds read limit")

// GetBounded checks actual streamed bytes, not a prior HEAD of a mutable key.
// The extra byte detects a provider that omits or misreports Content-Length.
func (c *Client) GetBounded(ctx context.Context, key string, limit int64) ([]byte, error) {
	if key == "" || limit < 1 || limit == 1<<63-1 {
		return nil, errors.New("invalid bounded object read")
	}
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		return nil, errors.New("object read failed")
	}
	defer out.Body.Close()
	if out.ContentLength != nil && *out.ContentLength > limit {
		return nil, ErrObjectTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(out.Body, limit+1))
	if err != nil {
		return nil, errors.New("object body read failed")
	}
	if int64(len(body)) > limit {
		return nil, ErrObjectTooLarge
	}
	return body, nil
}

// Put writes the bytes already verified by the API to a server-only final key.
// A failed response may follow a successful write; callers must not delete it.
func (c *Client) Put(ctx context.Context, key string, ciphertext []byte) error {
	if key == "" || len(ciphertext) == 0 {
		return errors.New("invalid object write")
	}
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(ciphertext), ContentLength: aws.Int64(int64(len(ciphertext)))})
	if err != nil {
		return errors.New("object write failed")
	}
	return nil
}

func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return ObjectInfo{Key: key}, nil
		}
		return ObjectInfo{}, fmt.Errorf("head object %q: %w", key, err)
	}
	return ObjectInfo{
		Key:         key,
		Exists:      true,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
	}, nil
}

// Open returns once the store has answered with headers, so a failure to start
// the read surfaces here and not midway through a caller's stream.
func (c *Client) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("get object %q: %w", key, err)
	}
	if out.ContentLength == nil {
		out.Body.Close()
		return nil, 0, fmt.Errorf("get object %q: no content length", key)
	}
	return out.Body, *out.ContentLength, nil
}

// Delete is idempotent: a 404 (already gone) is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var nfe *types.NotFound
	var nske *types.NoSuchKey
	return errors.As(err, &nfe) || errors.As(err, &nske)
}
