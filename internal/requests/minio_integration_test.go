//go:build integration

package requests

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

// Run with scripts/ci/storage-integration.sh. The service credentials and bucket
// are supplied by its isolated MinIO stack; no real deployment values are used.
func TestMinIORequestImmutableFinalizeAndLatePUT(t *testing.T) {
	ctx := context.Background()
	f, _ := largeFixture(t)
	objectStore, err := storage.New(storage.Config{Enabled: true, Endpoint: os.Getenv("FLICK_S3_ENDPOINT"), Region: os.Getenv("FLICK_S3_REGION"), Bucket: os.Getenv("FLICK_S3_BUCKET"), AccessKeyID: os.Getenv("FLICK_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("FLICK_S3_SECRET_ACCESS_KEY"), PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	f.store.opts.Objects = objectStore
	c := f.create(t)
	in, data := largeInput()
	reserved, err := f.store.Reserve(ctx, c.ID, c.SubmissionToken, in)
	if err != nil {
		t.Fatal(err)
	}
	var upload, final string
	if err := f.db.QueryRow(`select upload_key,final_key from requests where id=?`, c.ID).Scan(&upload, &final); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objectStore.Delete(ctx, upload); _ = objectStore.Delete(ctx, final) })
	put := func(body []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, reserved.Upload.Method, reserved.Upload.URL, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = int64(len(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("presigned upload failed")
		}
		resp.Body.Close()
		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			t.Fatalf("presigned status %d", resp.StatusCode)
		}
	}
	put(data)
	if _, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in)); err != nil {
		t.Fatal(err)
	}
	put(bytes.Repeat([]byte{3}, len(data)))
	opened, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
	if err != nil || opened.Ciphertext != base64.StdEncoding.EncodeToString(data) {
		t.Fatal("accepted final bytes changed", err)
	}
	for _, event := range outboxKeys(t, f.db) {
		if err := objectStore.Delete(ctx, event.ObjectKey); err != nil {
			t.Fatal(err)
		}
	}
	expired := c.ExpiresAt.Add(24 * time.Hour)
	f.store.now = func() time.Time { return expired }
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := PurgeExpiredTx(ctx, tx, expired, 10, f.store.opts.Outbox); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	put(data) // Logical clock is past retention; the provider still permits its URL.
	var found bool
	for range 100 {
		if _, err := f.store.ReconcileOnce(ctx, 1); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.db.QueryRow(`select count(*) from request_reconciliation_pending where object_key=?`, upload).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("late PUT after metadata purge was not reconciled")
	}
}
