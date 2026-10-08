package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var ErrInvalidCursor = errors.New("invalid object listing cursor")

type ObjectPage struct {
	Keys       []string
	NextCursor string
}

// List returns one bounded page. An empty NextCursor completes the current pass.
func (c *Client) List(ctx context.Context, prefix, cursor string, limit int) (ObjectPage, error) {
	if prefix == "" || limit < 1 || limit > 1000 {
		return ObjectPage{}, fmt.Errorf("invalid object listing bounds")
	}
	input := &s3.ListObjectsV2Input{Bucket: aws.String(c.cfg.Bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(int32(limit))}
	if cursor != "" {
		input.ContinuationToken = aws.String(cursor)
	}
	out, err := c.s3.ListObjectsV2(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			if cursor != "" && (apiErr.ErrorCode() == "InvalidArgument" || apiErr.ErrorCode() == "InvalidToken" || apiErr.ErrorCode() == "InvalidContinuationToken") {
				return ObjectPage{}, ErrInvalidCursor
			}
			return ObjectPage{}, fmt.Errorf("object listing failed: %s", apiErr.ErrorCode())
		}
		// Transport errors can contain the private endpoint or signed query URL.
		return ObjectPage{}, errors.New("object listing request failed")
	}
	if len(out.Contents) > limit {
		return ObjectPage{}, errors.New("object listing exceeded requested page size")
	}
	page := ObjectPage{Keys: make([]string, 0, len(out.Contents))}
	for _, object := range out.Contents {
		page.Keys = append(page.Keys, aws.ToString(object.Key))
	}
	if aws.ToBool(out.IsTruncated) {
		page.NextCursor = aws.ToString(out.NextContinuationToken)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return ObjectPage{}, errors.New("object listing cursor did not advance")
		}
	}
	return page, nil
}
