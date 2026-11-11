package secrets

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/requests"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

type listingObjects struct {
	storage.ObjectStore
	list func(context.Context, string, string, int) (storage.ObjectPage, error)
}

func (o listingObjects) List(ctx context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
	return o.list(ctx, prefix, cursor, limit)
}

func TestReconciliationResumesPaginationAndProtectsLiveObjects(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "api.db")
	conn, err := db.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := db.MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	objects := newMockObjectStore()
	store := newLargeTestStore(t, conn, objects)
	now := time.Date(2026, 10, 8, 1, 0, 0, 123, time.UTC)
	fixtures := []secretFixture{
		{id: "a-active", state: "active", expiresAt: now.Add(time.Hour), createdAt: now},
		{id: "b-pending", state: "pending_upload", expiresAt: now.Add(time.Hour), createdAt: now.Add(-15*time.Minute + time.Nanosecond)},
		{id: "c-expired", state: "active", expiresAt: now, createdAt: now.Add(-time.Hour)},
		{id: "d-abandoned", state: "pending_upload", expiresAt: now.Add(time.Hour), createdAt: now.Add(-15 * time.Minute)},
	}
	for _, f := range fixtures {
		f.storageBackend = StorageS3
		f.storageKey = managedObjectPrefix + f.id
		insertSecret(t, ctx, conn, f)
		objects.objects[f.storageKey] = []byte("ciphertext")
	}
	objects.objects[managedObjectPrefix+"e-orphan"] = []byte("ciphertext")
	objects.objects["legacy-key"] = []byte("ciphertext")
	objects.objects["managed/requests/foreign"] = []byte("ciphertext")
	reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 2)
	reaper.SetNowForTest(func() time.Time { return now })
	if n, err := reaper.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("live page: %d %v", n, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	conn, err = db.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	store = newLargeTestStore(t, conn, objects)
	reaper = newTestReaper(t, conn, store, newTestOutbox(t, conn), 2)
	reaper.SetNowForTest(func() time.Time { return now })
	for _, want := range []int{2, 1, 0, 0, 0} {
		if n, err := reaper.ReconcileOnce(ctx); err != nil || n != want {
			t.Fatalf("restarted pagination: %d want %d %v", n, want, err)
		}
	}
	jobs := readOutboxEvents(t, ctx, conn)
	if len(jobs) != 3 {
		t.Fatalf("duplicate or missing jobs: %d", len(jobs))
	}
	for _, job := range jobs {
		if job.ObjectKey == managedObjectPrefix+"a-active" || job.ObjectKey == managedObjectPrefix+"b-pending" || job.ObjectKey == "legacy-key" || job.ObjectKey == "managed/requests/foreign" {
			t.Fatal("protected object scheduled")
		}
	}
}

func TestReconciliationLatePUTAfterExpiryGetsFreshFencedJob(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	objects := newMockObjectStore()
	store := newLargeTestStore(t, conn, objects)
	store.outbox = newTestOutbox(t, conn)
	now := time.Date(2026, 10, 8, 1, 0, 0, 123, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindFile, EncryptedFilename: ageField(200), SizeBytes: 2048, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	key := managedObjectPrefix + created.ID
	objects.objects[key] = []byte("ciphertext")
	if _, err := store.Revoke(ctx, created.ID, created.ManagementToken); err != nil {
		t.Fatal(err)
	}
	immediate := readOutboxEvents(t, ctx, conn)[0]
	if err := objects.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	// The original signature and management authority can both expire before a
	// PUT accepted earlier finishes. No finite completion deadline is assumed.
	now = now.Add(24 * time.Hour)
	reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 2)
	reaper.SetNowForTest(func() time.Time { return now })
	if _, err := reaper.ClaimOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var managementRows int
	if err := conn.QueryRow(`select count(*) from secret_management`).Scan(&managementRows); err != nil || managementRows != 0 {
		t.Fatalf("management not purged: %d %v", managementRows, err)
	}
	objects.objects[key] = []byte("late ciphertext")
	if n, err := reaper.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("late object missed: %d %v", n, err)
	}
	var firstJob string
	if err := conn.QueryRow(`select job_id from object_reconciliation_pending_v2 where backend = 's3_object' and object_key = ?`, key).Scan(&firstJob); err != nil {
		t.Fatal(err)
	}
	if firstJob == immediate.JobID {
		t.Fatal("reused completed immediate job")
	}
	if err := store.AcknowledgeObjectCleanup(ctx, storage.PrimaryBackend, immediate.JobID, key); err != nil {
		t.Fatal(err)
	}
	if n, err := reaper.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("old ack cleared a newer claim: %d %v", n, err)
	}
	if err := objects.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.AcknowledgeObjectCleanup(ctx, storage.PrimaryBackend, firstJob, key); err != nil {
			t.Fatal(err)
		}
	}
	objects.objects[key] = []byte("another late ciphertext")
	if n, err := reaper.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("second late object missed: %d %v", n, err)
	}
	var secondJob string
	if err := conn.QueryRow(`select job_id from object_reconciliation_pending_v2 where backend = 's3_object' and object_key = ?`, key).Scan(&secondJob); err != nil {
		t.Fatal(err)
	}
	if secondJob == firstJob {
		t.Fatal("reused terminal reconciliation job")
	}
	if err := store.AcknowledgeObjectCleanup(ctx, storage.PrimaryBackend, firstJob, key); err != nil {
		t.Fatal(err)
	}
	var remaining string
	if err := conn.QueryRow(`select job_id from object_reconciliation_pending_v2 where backend = 's3_object' and object_key = ?`, key).Scan(&remaining); err != nil || remaining != secondJob {
		t.Fatalf("stale ack removed new job: %s %v", remaining, err)
	}
	if err := objects.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeObjectCleanup(ctx, storage.PrimaryBackend, secondJob, key); err != nil {
		t.Fatal(err)
	}
	if len(objects.objects) != 0 {
		t.Fatal("late ciphertext not deleted")
	}
}

func TestReconciliationFailuresDoNotAdvanceOrLoseClaims(t *testing.T) {
	for _, failure := range []string{"listing", "lookup", "outbox", "commit", "invalid cursor", "wrong prefix"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			objects := newMockObjectStore()
			store := newLargeTestStore(t, conn, objects)
			reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 1)
			_, err := conn.Exec(`update object_reconciliation_cursor_v2 set continuation_token = 'old' where backend = 's3_object'; insert into object_reconciliation_pending_v2 values ('s3_object','managed/secrets/existing','existing-job')`)
			if err != nil {
				t.Fatal(err)
			}
			wrapped, wrapErr := storage.NewRegistry(listingObjects{objects, func(_ context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
				if prefix != managedObjectPrefix || cursor != "old" || limit != 1 {
					t.Fatal("listing escaped its bounds")
				}
				if failure == "listing" {
					return storage.ObjectPage{}, errors.New("listing failed")
				}
				if failure == "invalid cursor" {
					return storage.ObjectPage{}, storage.ErrInvalidCursor
				}
				key := managedObjectPrefix + "orphan"
				if failure == "wrong prefix" {
					key = "managed/requests/foreign"
				}
				return storage.ObjectPage{Keys: []string{key}, NextCursor: "next"}, nil
			}}, nil)
			if wrapErr != nil {
				t.Fatal(wrapErr)
			}
			store.objects = wrapped
			switch failure {
			case "lookup":
				_, err = conn.Exec(`drop table secrets`)
			case "outbox":
				reaper.outbox = &fakeOutbox{err: errors.New("enqueue failed")}
			case "commit":
				_, err = conn.Exec(`create table fail_commit (id text references secrets(id) deferrable initially deferred); create trigger fail_claim after insert on object_reconciliation_pending_v2 begin insert into fail_commit values (new.job_id); end;`)
			}
			if err != nil {
				t.Fatal(err)
			}
			n, err := reaper.ReconcileOnce(ctx)
			if n != 0 || (err == nil) != (failure == "wrong prefix") {
				t.Fatalf("unexpected failure result %d %v", n, err)
			}
			var cursor string
			var generation int
			if err := conn.QueryRow(`select continuation_token,generation from object_reconciliation_cursor_v2 where backend = 's3_object'`).Scan(&cursor, &generation); err != nil {
				t.Fatal(err)
			}
			want := "old"
			wantGeneration := 0
			if failure == "invalid cursor" {
				want = ""
				wantGeneration = 1
			}
			if failure == "wrong prefix" {
				want = "next"
				wantGeneration = 1
			}
			if cursor != want || generation != wantGeneration {
				t.Fatalf("cursor advanced despite failure: %q %d", cursor, generation)
			}
			var pending int
			if err := conn.QueryRow(`select count(*) from object_reconciliation_pending_v2 where backend = 's3_object'`).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("pending claims changed: %d %v", pending, err)
			}
			if len(readOutboxEvents(t, ctx, conn)) != 0 {
				t.Fatal("failed page retained jobs")
			}
		})
	}
}

func TestReconciliationRunsAlongsideExpiryBacklogAndFencesStalePages(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	objects := newMockObjectStore()
	store := newLargeTestStore(t, conn, objects)
	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		insertSecret(t, ctx, conn, secretFixture{id: id, expiresAt: now.Add(-time.Hour), createdAt: now.Add(-2 * time.Hour)})
		objects.objects[managedObjectPrefix+id] = []byte("ciphertext")
	}
	reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 1)
	reaper.SetNowForTest(func() time.Time { return now })
	for range 3 {
		if n, err := reaper.ClaimOnce(ctx); err != nil || n != 1 {
			t.Fatalf("expiry tick: %d %v", n, err)
		}
	}
	if len(readOutboxEvents(t, ctx, conn)) != 3 {
		t.Fatal("expiry backlog starved listing pages")
	}
	// A second instance commits while the first page listing is in flight.
	wrapped, wrapErr := storage.NewRegistry(listingObjects{objects, func(ctx context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
		if _, err := conn.ExecContext(ctx, `update object_reconciliation_cursor_v2 set continuation_token = 'newer',generation=generation+1 where backend = 's3_object'`); err != nil {
			t.Fatal(err)
		}
		return storage.ObjectPage{Keys: []string{managedObjectPrefix + "stale"}, NextCursor: "stale"}, nil
	}}, nil)
	if wrapErr != nil {
		t.Fatal(wrapErr)
	}
	store.objects = wrapped
	if n, err := reaper.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("stale listing committed: %d %v", n, err)
	}
	var cursor string
	if err := conn.QueryRow(`select continuation_token from object_reconciliation_cursor_v2 where backend = 's3_object'`).Scan(&cursor); err != nil || cursor != "newer" {
		t.Fatalf("newer cursor lost: %s %v", cursor, err)
	}
}

type requestListingObjects struct {
	storage.RequestObjectStore
	list func(context.Context, string, string, int) (storage.ObjectPage, error)
}

func (o requestListingObjects) List(ctx context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
	return o.list(ctx, prefix, cursor, limit)
}

func TestReaperRequestScanIsIndependentOfExpiryAndSenderFailures(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	outbox := newTestOutbox(t, conn)
	senderFailure := errors.New("sender listing failed")
	senderCalled, requestCalled := false, false
	senderObjects := listingObjects{list: func(_ context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
		senderCalled = true
		if prefix != "managed/secrets/" || limit != 2 {
			t.Fatal("sender scope changed")
		}
		return storage.ObjectPage{}, senderFailure
	}}
	senderStore, err := NewStore(conn, StoreOptions{PayloadInlineMaxBytes: 32, MaxObjectBytes: 116, MinTTLSeconds: 300, MaxTTLSeconds: 3600, Objects: testRegistry(senderObjects), Outbox: outbox})
	if err != nil {
		t.Fatal(err)
	}
	requestObjects := requestListingObjects{list: func(_ context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
		requestCalled = true
		if prefix != requests.ObjectPrefix || limit != 2 {
			t.Fatal("request scan unbounded or wrong scope")
		}
		return storage.ObjectPage{Keys: []string{requests.ObjectPrefix + "orphan"}}, nil
	}}
	requestStore, err := requests.NewStore(conn, requests.Options{PayloadInlineMaxBytes: 32, MaxObjectBytes: 100, MinTTLSeconds: 300, DefaultTTLSeconds: 600, MaxTTLSeconds: 3600, Objects: requestTestRegistry(requestObjects), Outbox: outbox})
	if err != nil {
		t.Fatal(err)
	}
	reaper, err := NewReaper(conn, senderStore, outbox, ReaperOptions{BatchSize: 2, Requests: requestStore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`drop table secret_management`); err != nil {
		t.Fatal(err)
	}
	if _, err := reaper.ClaimOnce(ctx); err == nil || !errors.Is(err, senderFailure) {
		t.Fatal("reaper swallowed expiry/listing error", err)
	}
	if !senderCalled || !requestCalled {
		t.Fatal("failed component starved other scanner")
	}
	jobs := readOutboxEvents(t, ctx, conn)
	if len(jobs) != 1 || jobs[0].ObjectKey != requests.ObjectPrefix+"orphan" {
		t.Fatal("request scan did not commit independently", jobs)
	}
}

// A consumed S3 body may still be streaming to its recipient, so
// reconciliation leaves it alone for openObjectGrace after consumed_at and
// deletes it as an orphan once that has passed.
func TestReconciliationProtectsOpenedObjectForStreamGrace(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 123, time.UTC)
	for _, tc := range []struct {
		name       string
		consumedAt time.Time
		wantJobs   int
	}{
		{"just opened", now, 0},
		{"one nanosecond inside the grace", now.Add(-openObjectGrace + time.Nanosecond), 0},
		{"grace elapsed", now.Add(-openObjectGrace), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			objects := newMockObjectStore()
			store := newLargeTestStore(t, conn, objects)
			f := secretFixture{
				id: "opened", storageBackend: StorageS3, storageKey: managedObjectPrefix + "opened",
				expiresAt: now.Add(time.Hour), consumedAt: tc.consumedAt, createdAt: now.Add(-time.Hour),
			}
			insertSecret(t, ctx, conn, f)
			objects.objects[f.storageKey] = ageBody(100)
			reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 10)
			reaper.SetNowForTest(func() time.Time { return now })
			if n, err := reaper.ReconcileOnce(ctx); err != nil || n != tc.wantJobs {
				t.Fatalf("reconciled %d want %d: %v", n, tc.wantJobs, err)
			}
			jobs := readOutboxEvents(t, ctx, conn)
			if len(jobs) != tc.wantJobs || (tc.wantJobs == 1 && jobs[0].ObjectKey != f.storageKey) {
				t.Fatalf("object delete jobs = %+v, want %d for %s", jobs, tc.wantJobs, f.storageKey)
			}
		})
	}
}

// 열람은 secrets 행과 관리 행을 즉시 지운다. 그래도 수신자에게 아직 스트리밍 중일 수
// 있는 객체는 객체 키와 유예 마감만 담은 secret_open_grace 행이 openObjectGrace 동안
// 재조정(reconciliation)으로부터 보호하고, 유예가 지나면 객체는 고아로 삭제 예약되며
// 유예 행도 reaper가 지운다.
func TestOpenedObjectStaysProtectedForStreamGraceWithoutSecretRow(t *testing.T) {
	// Given: 방금 열람한 S3 시크릿
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 1, 0, 0, 123, time.UTC)
	conn := openTestDB(t, ctx)
	objects := newMockObjectStore()
	store := newLargeTestStore(t, conn, objects)
	store.SetNowForTest(func() time.Time { return now })
	created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindFile, EncryptedFilename: ageField(200), SizeBytes: 100, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	key := managedObjectPrefix + created.ID
	objects.objects[key] = ageBody(100)
	if err := store.Finalize(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	opened, err := openS3(t, ctx, conn, store, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	opened.Body.Close()

	// Then: 시크릿 ID로 찾을 수 있는 행은 없고, 유예 행은 키와 마감만 가진다
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`select count(*) from secrets where id = ?`, created.ID) + count(`select count(*) from secret_management where secret_id = ?`, created.ID); n != 0 {
		t.Fatalf("%d rows still remember the opened ID", n)
	}
	if n := count(`select count(*) from pragma_table_info('secret_open_grace') where name not in ('object_key', 'grace_until')`); n != 0 {
		t.Fatalf("secret_open_grace has %d columns beyond the key and deadline", n)
	}
	if n := count(`select count(*) from secret_open_grace where object_key = ?`, key); n != 1 {
		t.Fatalf("grace rows for the streaming object = %d, want 1", n)
	}

	// When: reaper가 유예 직전과 유예 마감에 각각 한 번씩 돈다
	reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 10)
	tick := func(at time.Time) int {
		t.Helper()
		reaper.SetNowForTest(func() time.Time { return at })
		if _, err := reaper.ClaimOnce(ctx); err != nil {
			t.Fatal(err)
		}
		return len(readOutboxEvents(t, ctx, conn))
	}
	if n := tick(now.Add(openObjectGrace - time.Nanosecond)); n != 0 {
		t.Fatalf("%d object delete jobs while the body may still stream, want 0", n)
	}
	if n := count(`select count(*) from secret_open_grace`); n != 1 {
		t.Fatalf("grace row purged before its deadline (%d rows)", n)
	}
	if n := tick(now.Add(openObjectGrace)); n != 1 {
		t.Fatalf("%d object delete jobs after the grace, want 1", n)
	}
	if jobs := readOutboxEvents(t, ctx, conn); jobs[0].ObjectKey != key {
		t.Fatalf("deleted %q, want %q", jobs[0].ObjectKey, key)
	}
	if n := count(`select count(*) from secret_open_grace`); n != 0 {
		t.Fatalf("grace rows after the deadline = %d, want purged", n)
	}
}

// requestTestRegistry wraps a requests-side test double as a single-backend
// storage registry. The error is unreachable for a non-nil primary.
func requestTestRegistry(primary storage.ObjectStore) *storage.Registry {
	registry, err := storage.NewRegistry(primary, nil)
	if err != nil {
		panic(err)
	}
	return registry
}
