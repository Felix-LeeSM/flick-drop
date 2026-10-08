package secrets

import (
	"context"
	"testing"
	"time"
)

func TestManagementPendingFractionalDeadline(t *testing.T) {
	for _, tc := range []struct {
		name             string
		createdNS, nowNS int
	}{
		{"no cutoff fraction", 123456789, 0},
		{"shorter cutoff fraction", 123456789, 123000000},
		{"one nanosecond before", 123456789, 123456788},
		{"exact fractional deadline", 123456789, 123456789},
		{"longer cutoff fraction", 123000000, 123456789},
		{"before whole second", 0, -1},
		{"exact whole second", 0, 0},
		{"after whole second", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			objects := newMockObjectStore()
			store := newLargeTestStore(t, conn, objects)
			base := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
			now := base.Add(time.Duration(tc.createdNS))
			deadline := now.Add(15 * time.Minute)
			store.SetNowForTest(func() time.Time { return now })
			filename := "encrypted-name"
			created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindFile, EncryptedFilename: &filename, Nonce: "nonce", SizeBytes: 2048, TTLSeconds: 3600})
			if err != nil {
				t.Fatal(err)
			}
			objects.objects["managed/secrets/"+created.ID] = make([]byte, 2048+AEADOverheadBytes)
			now = base.Add(15 * time.Minute).Add(time.Duration(tc.nowNS))
			reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 1)
			reaper.SetNowForTest(func() time.Time { return now })
			want := 0
			if !now.Before(deadline) {
				want = 1
			}
			if got, err := reaper.ClaimOnce(ctx); err != nil || got != want {
				t.Fatalf("now=%s deadline=%s: reclaimed=%d want=%d err=%v", formatTime(now), formatTime(deadline), got, want, err)
			}
			status, err := store.Management(ctx, created.ID, created.ManagementToken)
			if err != nil {
				t.Fatal(err)
			}
			if want == 0 {
				if status.Status != "pending_upload" || !status.CanCancel {
					t.Fatalf("valid pending status: %+v", status)
				}
				if err := store.Finalize(ctx, created.ID); err != nil {
					t.Fatalf("valid pending finalize: %v", err)
				}
			} else if status.Status != "unavailable" || status.CanCancel {
				t.Fatalf("elapsed pending status: %+v", status)
			}
		})
	}
}
