package storage

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testPresignClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		Enabled:         true,
		Endpoint:        "http://localhost:9000",
		Region:          "us-east-1",
		Bucket:          "flick-dev",
		AccessKeyID:     "AKIDTEST",
		SecretAccessKey: "secrettest",
		PathStyle:       true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.SetNowForTest(func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) })
	return c
}

func TestPresignPUTShape(t *testing.T) {
	c := testPresignClient(t)

	upload, err := c.PresignPUT(context.Background(), "obj-1", 1024, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	if upload.Method != "PUT" {
		t.Fatalf("method = %q, want PUT", upload.Method)
	}
	if upload.Headers["Content-Length"] != "1024" {
		t.Fatalf("Content-Length header = %q, want 1024", upload.Headers["Content-Length"])
	}
	if want := time.Date(2026, 1, 2, 3, 9, 5, 0, time.UTC); !upload.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", upload.ExpiresAt, want)
	}

	u, err := url.Parse(upload.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Path != "/flick-dev/obj-1" {
		t.Fatalf("path = %q, want /flick-dev/obj-1 (path-style, key pinned)", u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		t.Fatalf("algorithm = %q", q.Get("X-Amz-Algorithm"))
	}
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), "AKIDTEST/") {
		t.Fatalf("credential = %q", q.Get("X-Amz-Credential"))
	}
	if q.Get("X-Amz-Expires") != "300" {
		t.Fatalf("expires = %q, want 300", q.Get("X-Amz-Expires"))
	}
	// The whole point of signing Content-Length: a body of any other size fails
	// authentication at the bucket instead of landing and being caught later.
	signed := q.Get("X-Amz-SignedHeaders")
	if !strings.Contains(signed, "content-length") {
		t.Fatalf("signed headers = %q, want content-length included", signed)
	}
	if len(q.Get("X-Amz-Signature")) != 64 {
		t.Fatalf("signature len = %d, want 64", len(q.Get("X-Amz-Signature")))
	}
}

// A different size must produce a different signature — otherwise the length
// would not actually be bound to it.
func TestPresignPUTSignatureCoversSize(t *testing.T) {
	c := testPresignClient(t)
	ctx := context.Background()

	a, err := c.PresignPUT(ctx, "obj-1", 1024, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign a: %v", err)
	}
	b, err := c.PresignPUT(ctx, "obj-1", 1025, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign b: %v", err)
	}

	sigA, _ := url.Parse(a.URL)
	sigB, _ := url.Parse(b.URL)
	if sigA.Query().Get("X-Amz-Signature") == sigB.Query().Get("X-Amz-Signature") {
		t.Fatal("signature is identical for different sizes; Content-Length is not signed")
	}
}

func TestPresignPUTRejectsBadInput(t *testing.T) {
	c := testPresignClient(t)
	ctx := context.Background()

	if _, err := c.PresignPUT(ctx, "", 1024, time.Minute); err == nil {
		t.Fatal("expected an error for an empty key")
	}
	if _, err := c.PresignPUT(ctx, "obj-1", 0, time.Minute); err == nil {
		t.Fatal("expected an error for a zero size")
	}
	if _, err := c.PresignPUT(ctx, "obj-1", -1, time.Minute); err == nil {
		t.Fatal("expected an error for a negative size")
	}
}
