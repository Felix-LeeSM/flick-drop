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

func TestPutStreamSendsExactBytesWithLength(t *testing.T) {
	for _, kind := range []string{"exact", "short", "refused", "transport"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte("age-encryption.org/v1\nciphertext")
			puts := 0
			client := &Client{cfg: Config{Bucket: "test-bucket"}, s3: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String("https://object.example"), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: listTransport(func(r *http.Request) (*http.Response, error) {
				puts++
				if kind == "transport" {
					return nil, errors.New("https://private.example/?signed=private")
				}
				sent, err := io.ReadAll(r.Body)
				// A streamed aws-chunked body carries trailers some S3-compatible
				// stores reject; the spool makes the request a plain sized PUT.
				if err != nil || r.Method != "PUT" || !bytes.Equal(sent, body) || r.ContentLength != int64(len(body)) || strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
					t.Fatalf("PUT changed bytes or framing: %v %q %d %q", err, sent, r.ContentLength, r.Header.Get("Content-Encoding"))
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			})})}
			size := int64(len(body))
			if kind == "short" {
				size++
			}
			refusal := errors.New("checksum mismatch")
			verified := false
			verify := func() error {
				verified = true
				if kind == "refused" {
					return refusal
				}
				return nil
			}
			err := client.PutStream(context.Background(), "managed/requests/final", bytes.NewReader(body), size, verify)
			switch kind {
			case "exact":
				if err != nil || puts != 1 || !verified {
					t.Fatal(err, puts, verified)
				}
			case "refused":
				// A refused body is never written, and the refusal is not
				// masked as a storage failure.
				if err != refusal || puts != 0 {
					t.Fatal("refused body was written", err, puts)
				}
			case "short":
				if err == nil || puts != 0 || verified {
					t.Fatal("short body was written", err, puts)
				}
			case "transport":
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatal("transport leak", err)
				}
			}
		})
	}
}

func TestPutStreamRefusesNilVerify(t *testing.T) {
	body := []byte("age-encryption.org/v1\nciphertext")
	puts := 0
	client := &Client{cfg: Config{Bucket: "test-bucket"}, s3: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String("https://object.example"), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: listTransport(func(r *http.Request) (*http.Response, error) {
		puts++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})}
	if err := client.PutStream(context.Background(), "managed/requests/final", bytes.NewReader(body), int64(len(body)), nil); err == nil || puts != 0 {
		t.Fatal("nil verify was accepted", err, puts)
	}
}
