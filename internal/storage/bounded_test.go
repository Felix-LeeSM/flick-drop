package storage

import (
	"bytes"
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

func TestBoundedGETChecksStreamAndPUTUsesSameBytes(t *testing.T) {
	for _, kind := range []string{"exact", "oversize_without_length", "oversize_advertised", "transport"} {
		t.Run(kind, func(t *testing.T) {
			closed := false
			read := 0
			client := &Client{cfg: Config{Bucket: "test-bucket"}, s3: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String("https://object.example"), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: listTransport(func(r *http.Request) (*http.Response, error) {
				if kind == "transport" {
					return nil, errors.New("https://private.example/?signed=private")
				}
				body := []byte("ciphertext")
				if kind != "exact" {
					body = bytes.Repeat([]byte{1}, 1000)
				}
				if r.Method == "PUT" {
					sent, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(sent, body) || r.Header.Get("X-Amz-Copy-Source") != "" {
						t.Fatal("PUT changed bytes or copied mutable source")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
				}
				headers := make(http.Header)
				if kind == "oversize_advertised" {
					headers.Set("Content-Length", "1000")
				}
				return &http.Response{StatusCode: 200, Header: headers, Body: &trackedBody{Reader: bytes.NewReader(body), closed: &closed, read: &read}}, nil
			})})}
			body, err := client.GetBounded(context.Background(), "managed/requests/staging", 10)
			if kind == "exact" {
				if err != nil || string(body) != "ciphertext" {
					t.Fatal(err)
				}
				if err := client.Put(context.Background(), "managed/requests/final", body); err != nil {
					t.Fatal(err)
				}
			} else if kind == "transport" {
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatal("transport leak", err)
				}
			} else if !errors.Is(err, ErrObjectTooLarge) {
				t.Fatal("oversize accepted", err)
			}
			if kind != "transport" && (!closed || read > 11) {
				t.Fatalf("unbounded or unclosed read: %d %v", read, closed)
			}
		})
	}
}

type trackedBody struct {
	*bytes.Reader
	closed *bool
	read   *int
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	*b.read += n
	return n, err
}
func (b *trackedBody) Close() error { *b.closed = true; return nil }
