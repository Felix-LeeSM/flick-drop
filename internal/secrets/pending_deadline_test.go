package secrets

import (
	"context"
	"testing"
	"time"
)

// timestamptz는 마이크로초 정밀도로 저장하므로 대기 마감 경계는 마이크로초 단위로
// 확인한다. SQLite 때 나노초 단위로 확인하던 경계 포함 여부(<=)는 같은 의미다.
func TestManagementPendingFractionalDeadline(t *testing.T) {
	for _, tc := range []struct {
		name             string
		createdUs, nowUs int
	}{
		{"no cutoff fraction", 123456, 0},
		{"shorter cutoff fraction", 123456, 123},
		{"one microsecond before", 123456, 123455},
		{"exact fractional deadline", 123456, 123456},
		{"longer cutoff fraction", 123, 123456},
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
			now := base.Add(time.Duration(tc.createdUs) * time.Microsecond)
			deadline := now.Add(15 * time.Minute)
			store.SetNowForTest(func() time.Time { return now })
			created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindFile, EncryptedFilename: ageField(200), SizeBytes: 2048, TTLSeconds: 3600})
			if err != nil {
				t.Fatal(err)
			}
			objects.objects["managed/secrets/"+created.ID] = ageBody(2048)
			now = base.Add(15 * time.Minute).Add(time.Duration(tc.nowUs) * time.Microsecond)
			reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 1)
			reaper.SetNowForTest(func() time.Time { return now })
			want := 0
			if !now.Before(deadline) {
				want = 1
			}
			if got, err := reaper.ClaimOnce(ctx); err != nil || got != want {
				t.Fatalf("now=%s deadline=%s: reclaimed=%d want=%d err=%v", now.Format(time.RFC3339Nano), deadline.Format(time.RFC3339Nano), got, want, err)
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
