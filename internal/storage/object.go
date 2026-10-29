package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// PutStream writes exactly size bytes from body to a server-only final key.
// The body is spooled to a temporary file first: the SDK signs and checksums a
// seekable body the same way as before, while an unseekable stream would need
// aws-chunked trailing checksums that S3-compatible stores do not all accept.
// Memory stays bounded; the spool holds only ciphertext and is removed. A failed
// response may follow a successful write; callers must not delete it.
func (c *Client) PutStream(ctx context.Context, key string, body io.Reader, size int64) error {
	if key == "" || size < 1 {
		return errors.New("invalid object write")
	}
	spool, err := os.CreateTemp("", "flick-object-*")
	if err != nil {
		return errors.New("object spool failed")
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	written, err := io.Copy(spool, io.LimitReader(body, size))
	if err != nil || written != size {
		return errors.New("object body read failed")
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return errors.New("object spool failed")
	}
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key), Body: spool, ContentLength: aws.Int64(size)})
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
