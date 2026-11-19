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

// Spools to a temp file: an unseekable body would need aws-chunked trailing checksums
// that not all S3-compatible stores accept. A failed response may follow a successful write; callers must not delete it.
func (c *Client) PutStream(ctx context.Context, key string, body io.Reader, size int64, verify func() error) error {
	if key == "" || size < 1 || verify == nil {
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
	if err := verify(); err != nil {
		return err
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
		return ObjectInfo{}, fmt.Errorf("head object: %w", err)
	}
	return ObjectInfo{
		Key:         key,
		Exists:      true,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
	}, nil
}

// Returns after response headers so a failed read start surfaces here, not mid-stream.
func (c *Client) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("get object: %w", err)
	}
	if out.ContentLength == nil {
		out.Body.Close()
		return nil, 0, fmt.Errorf("get object: no content length")
	}
	return out.Body, *out.ContentLength, nil
}

// Idempotent: a 404 is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func isNotFound(err error) bool {
	var nfe *types.NotFound
	var nske *types.NoSuchKey
	return errors.As(err, &nfe) || errors.As(err, &nske)
}
