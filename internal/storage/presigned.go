package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// PresignPOST returns a presigned POST form whose policy pins the object key
// and enforces content-length-range [0, maxSize] — the bucket rejects an
// oversized upload with 413 before any bytes land. Go's AWS SDK does not build
// presigned POST, so the SigV4 POST signature is derived manually: the policy
// (base64) is the string-to-sign, signed with the standard 4-step key chain.
func (c *Client) PresignPOST(_ context.Context, key string, maxSize int64, ttl time.Duration) (POSTForm, error) {
	if key == "" {
		return POSTForm{}, fmt.Errorf("object key is required")
	}
	if maxSize <= 0 {
		return POSTForm{}, fmt.Errorf("max size must be positive")
	}

	now := c.now().UTC()
	expiration := now.Add(ttl)
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	credential := fmt.Sprintf("%s/%s/%s/s3/aws4_request", c.cfg.AccessKeyID, date, c.cfg.Region)

	policy := postPolicy{
		Expiration: expiration.Format("2006-01-02T15:04:05.000Z"),
		Conditions: []any{
			map[string]string{"bucket": c.cfg.Bucket},
			map[string]string{"key": key},
			[]any{"content-length-range", int64(0), maxSize},
			map[string]string{"x-amz-credential": credential},
			map[string]string{"x-amz-algorithm": "AWS4-HMAC-SHA256"},
			map[string]string{"x-amz-date": amzDate},
		},
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return POSTForm{}, fmt.Errorf("marshal post policy: %w", err)
	}
	policyB64 := base64.StdEncoding.EncodeToString(policyJSON)

	signature := signPOSTPolicy(c.cfg.SecretAccessKey, date, c.cfg.Region, policyB64)

	return POSTForm{
		URL:       c.postURL(),
		Method:    "POST",
		ExpiresAt: expiration,
		Fields: map[string]string{
			"key":              key,
			"policy":           policyB64,
			"x-amz-algorithm":  "AWS4-HMAC-SHA256",
			"x-amz-credential": credential,
			"x-amz-date":       amzDate,
			"x-amz-signature":  signature,
		},
		FileField: "file",
	}, nil
}

type postPolicy struct {
	Expiration string `json:"expiration"`
	Conditions []any  `json:"conditions"`
}

func signPOSTPolicy(secret, date, region, policyB64 string) string {
	key := deriveSigningKey(secret, date, region, "s3")
	return hex.EncodeToString(hmacSHA256(key, []byte(policyB64)))
}

// deriveSigningKey is the standard SigV4 4-step HMAC chain:
// HMAC(HMAC(HMAC(HMAC("AWS4"+secret, date), region), service), "aws4_request").
func deriveSigningKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
