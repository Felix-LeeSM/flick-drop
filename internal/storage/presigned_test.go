package storage

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestDeriveSigningKeyAWSVector(t *testing.T) {
	// AWS SigV4 reference example: region=us-east-1, service=iam, date=20150830,
	// secret=wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY. The expected signing key
	// is published in the AWS SigV4 documentation.
	got := deriveSigningKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20150830", "us-east-1", "iam")
	want := "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	if hex.EncodeToString(got) != want {
		t.Fatalf("signing key = %s, want %s", hex.EncodeToString(got), want)
	}
}

func TestPresignPOSTShape(t *testing.T) {
	c := &Client{
		cfg: Config{
			Enabled:         true,
			Endpoint:        "http://localhost:9000",
			Region:          "us-east-1",
			Bucket:          "flick-dev",
			AccessKeyID:     "AKIDTEST",
			SecretAccessKey: "secrettest",
			PathStyle:       true,
		},
		now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}

	form, err := c.PresignPOST(context.Background(), "obj-1", 1024, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	if form.Method != "POST" || form.FileField != "file" {
		t.Fatalf("method/filefield = %q/%q", form.Method, form.FileField)
	}
	if form.URL != "http://localhost:9000/flick-dev" {
		t.Fatalf("url = %q", form.URL)
	}
	if want := time.Date(2026, 1, 2, 3, 9, 5, 0, time.UTC); !form.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", form.ExpiresAt, want)
	}
	if got := form.Fields["x-amz-credential"]; got != "AKIDTEST/20260102/us-east-1/s3/aws4_request" {
		t.Fatalf("credential = %q", got)
	}
	if form.Fields["x-amz-algorithm"] != "AWS4-HMAC-SHA256" {
		t.Fatalf("algorithm = %q", form.Fields["x-amz-algorithm"])
	}
	sig := form.Fields["x-amz-signature"]
	if len(sig) != 64 {
		t.Fatalf("signature len = %d, want 64", len(sig))
	}

	// policy decodes to a document pinning key + content-length-range.
	var policy postPolicy
	raw, err := base64.StdEncoding.DecodeString(form.Fields["policy"])
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	if policy.Expiration != "2026-01-02T03:09:05.000Z" {
		t.Fatalf("expiration = %q", policy.Expiration)
	}
	if !policyHasContentLengthRange(policy) {
		t.Fatalf("policy missing content-length-range: %v", policy.Conditions)
	}

	// deterministic: same inputs → same signature (regression anchor).
	form2, err := c.PresignPOST(context.Background(), "obj-1", 1024, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign again: %v", err)
	}
	if form2.Fields["x-amz-signature"] != sig {
		t.Fatalf("signature not deterministic")
	}
	// different key → different signature.
	form3, err := c.PresignPOST(context.Background(), "obj-2", 1024, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign other: %v", err)
	}
	if form3.Fields["x-amz-signature"] == sig {
		t.Fatalf("signature unchanged for different key")
	}
}

func policyHasContentLengthRange(policy postPolicy) bool {
	for _, cond := range policy.Conditions {
		arr, ok := cond.([]any)
		if !ok || len(arr) != 3 {
			continue
		}
		if name, ok := arr[0].(string); ok && name == "content-length-range" {
			return true
		}
	}
	return false
}
