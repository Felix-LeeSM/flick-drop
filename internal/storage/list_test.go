package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type listTransport func(*http.Request) (*http.Response, error)

func (f listTransport) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestListObjectsBoundsPaginationAndInvalidCursor(t *testing.T) {
	for _, kind := range []string{"page", "complete", "invalid", "denied", "transport", "oversize", "stuck"} {
		t.Run(kind, func(t *testing.T) {
			client := &Client{cfg: Config{Bucket: "test-bucket"}, s3: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String("http://storage.example"), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: listTransport(func(r *http.Request) (*http.Response, error) {
				q := r.URL.Query()
				if q.Get("list-type") != "2" || q.Get("prefix") != "managed/secrets/" || q.Get("continuation-token") != "cursor" || q.Get("max-keys") != "1" {
					t.Fatalf("incorrect listing request")
				}
				code := http.StatusOK
				body := `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>managed/secrets/key</Key></Contents></ListBucketResult>`
				switch kind {
				case "complete":
					body = `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>managed/secrets/key</Key></Contents></ListBucketResult>`
				case "invalid":
					code = http.StatusBadRequest
					body = `<Error><Code>InvalidArgument</Code><Message>invalid cursor</Message></Error>`
				case "denied":
					code = http.StatusForbidden
					body = `<Error><Code>AccessDenied</Code></Error>`
				case "transport":
					return nil, errors.New("http://private.example/?signed=private")
				case "oversize":
					body = `<ListBucketResult><Contents><Key>one</Key></Contents><Contents><Key>two</Key></Contents></ListBucketResult>`
				case "stuck":
					body = `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>cursor</NextContinuationToken></ListBucketResult>`
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})})}
			page, err := client.List(context.Background(), "managed/secrets/", "cursor", 1)
			if kind == "page" || kind == "complete" {
				if err != nil || len(page.Keys) != 1 || page.Keys[0] != "managed/secrets/key" {
					t.Fatalf("bad page %v %v", page, err)
				}
				if (page.NextCursor != "") != (kind == "page") {
					t.Fatal("incorrect page completion")
				}
			} else if err == nil {
				t.Fatal("listing failure treated as empty page")
			}
			if kind == "invalid" && !errors.Is(err, ErrInvalidCursor) {
				t.Fatal("cursor error not recognizable")
			}
			if kind == "transport" && strings.Contains(err.Error(), "private") {
				t.Fatal("private endpoint leaked")
			}
		})
	}
}
