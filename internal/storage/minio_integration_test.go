//go:build integration

// Integration tests run against a real MinIO brought up by `docker compose up
// minio createbuckets`. Run with: go test -tags integration ./internal/storage/
// They verify the manual presigned POST signing actually interoperates with an
// S3-compatible bucket — the one thing unit tests cannot prove.

package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		Enabled:         true,
		Endpoint:        getenv("FLICK_S3_ENDPOINT", "http://127.0.0.1:9000"),
		Region:          getenv("FLICK_S3_REGION", "us-east-1"),
		Bucket:          getenv("FLICK_S3_BUCKET", "flick-dev"),
		AccessKeyID:     getenv("FLICK_S3_ACCESS_KEY_ID", "minioadmin"),
		SecretAccessKey: getenv("FLICK_S3_SECRET_ACCESS_KEY", "minioadmin"),
		PathStyle:       true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func nowSuffix(t *testing.T) string {
	t.Helper()
	return time.Now().Format("20060102-150405.000000")
}

// uploadViaPUT mimics the browser: the raw ciphertext as the request body,
// with Content-Length exactly as signed.
func uploadViaPUT(t *testing.T, upload UploadInstruction, payload []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(upload.Method, upload.URL, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.ContentLength = int64(len(payload))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do upload: %v", err)
	}
	return resp
}

func TestMinIORoundTrip(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	key := "it-roundtrip-" + nowSuffix(t)
	ciphertext := []byte("integration ciphertext payload")

	upload, err := c.PresignPUT(ctx, key, int64(len(ciphertext)), 5*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	resp := uploadViaPUT(t, upload, ciphertext)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status = %d, body = %s", resp.StatusCode, b)
	}

	info, err := c.Head(ctx, key)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !info.Exists || info.Size != int64(len(ciphertext)) {
		t.Fatalf("head = %+v, want exists size %d", info, len(ciphertext))
	}

	got, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, ciphertext) {
		t.Fatalf("get mismatch: %q", got)
	}

	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if info, _ := c.Head(ctx, key); info.Exists {
		t.Fatal("object still exists after delete")
	}
}

func TestMinIORejectsOversized(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	key := "it-oversized-" + nowSuffix(t)

	// The signature pins Content-Length to 8; the client tries to upload 64, so
	// authentication fails and no object lands.
	upload, err := c.PresignPUT(ctx, key, 8, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	resp := uploadViaPUT(t, upload, make([]byte, 64))
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("oversized upload accepted: status %d body %s", resp.StatusCode, b)
	}

	if info, _ := c.Head(ctx, key); info.Exists {
		t.Fatal("oversized object should not have landed in the bucket")
	}
}
