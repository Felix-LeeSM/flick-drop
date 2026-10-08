package requests

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

type requestObjects struct {
	mu       sync.Mutex
	bodies   map[string][]byte
	getHook  func(string)
	putHook  func(string) error
	listHook func(string, string, int) (storage.ObjectPage, error)
}

func (o *requestObjects) PresignPUT(_ context.Context, key string, size int64, ttl time.Duration) (storage.UploadInstruction, error) {
	return storage.UploadInstruction{URL: "https://object.example/" + key, Method: "PUT", Headers: map[string]string{"Content-Length": fmt.Sprint(size)}, ExpiresAt: time.Now().Add(ttl)}, nil
}
func (o *requestObjects) write(key string, b []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bodies[key] = bytes.Clone(b)
}
func (o *requestObjects) body(key string) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return bytes.Clone(o.bodies[key])
}
func (o *requestObjects) GetBounded(_ context.Context, key string, limit int64) ([]byte, error) {
	if o.getHook != nil {
		o.getHook(key)
	}
	b := o.body(key)
	if b == nil {
		return nil, errors.New("missing object")
	}
	if int64(len(b)) > limit {
		return nil, storage.ErrObjectTooLarge
	}
	return b, nil
}
func (o *requestObjects) Get(context.Context, string) ([]byte, error) { panic("unbounded GET used") }
func (o *requestObjects) Put(_ context.Context, key string, b []byte) error {
	o.write(key, b)
	if o.putHook != nil {
		return o.putHook(key)
	}
	return nil
}
func (o *requestObjects) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	b := o.body(key)
	return storage.ObjectInfo{Key: key, Exists: b != nil, Size: int64(len(b))}, nil
}
func (o *requestObjects) Delete(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.bodies, key)
	return nil
}
func (o *requestObjects) List(_ context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
	if o.listHook != nil {
		return o.listHook(prefix, cursor, limit)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	keys := []string{}
	for key := range o.bodies {
		if strings.HasPrefix(key, prefix) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	page := storage.ObjectPage{Keys: keys}
	if len(keys) > limit {
		page.Keys = keys[:limit]
		page.NextCursor = page.Keys[limit-1]
	}
	return page, nil
}
func largeFixture(t *testing.T) (fixture, *requestObjects) {
	t.Helper()
	f := newFixture(t)
	o := &requestObjects{bodies: map[string][]byte{}}
	outbox, err := events.NewOutboxStore(f.db, "flick.jobs")
	if err != nil {
		t.Fatal(err)
	}
	f.store.opts.PayloadInlineMaxBytes = 32
	f.store.opts.MaxFileBytes = 100
	f.store.opts.Objects = o
	f.store.opts.Outbox = outbox
	return f, o
}
func largeInput() (UploadInput, []byte) {
	in := submission("file")
	data := bytes.Repeat([]byte{9}, 40)
	digest := sha256.Sum256(data)
	return UploadInput{Generation: 1, AttemptToken: in.AttemptToken, Kind: "file", SizeBytes: 24, Envelope: in.Envelope, CiphertextSHA256: base64.StdEncoding.EncodeToString(digest[:])}, data
}
func reserve(t *testing.T, f fixture, o *requestObjects, c Created, in UploadInput, data []byte) (string, string, UploadResult) {
	t.Helper()
	result, err := f.store.Reserve(context.Background(), c.ID, c.SubmissionToken, in)
	if err != nil {
		t.Fatal(err)
	}
	var upload, final string
	if err := f.db.QueryRow(`select upload_key, final_key from requests where id=?`, c.ID).Scan(&upload, &final); err != nil {
		t.Fatal(err)
	}
	if upload == final || !strings.HasPrefix(upload, ObjectPrefix) || !strings.HasPrefix(final, ObjectPrefix) || strings.Contains(result.Upload.URL, final) {
		t.Fatal("mutable and final keys not separated")
	}
	o.write(upload, data)
	return upload, final, result
}
func attemptInput(in UploadInput) AttemptInput { return AttemptInput{in.Generation, in.AttemptToken} }
func reopenLarge(t *testing.T, f fixture) *Store {
	t.Helper()
	conn, err := db.OpenSQLite(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	store, err := NewStore(conn, f.store.opts)
	if err != nil {
		t.Fatal(err)
	}
	store.now = f.store.now
	return store
}
func outboxKeys(t *testing.T, conn *sql.DB) []events.JobEvent {
	t.Helper()
	rows, err := conn.Query(`select payload_json from outbox_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var eventsOut []events.JobEvent
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var event events.JobEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		eventsOut = append(eventsOut, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return eventsOut
}

func TestLargeReservationFinalizeAndOneTimeOpen(t *testing.T) {
	ctx := context.Background()
	f, o := largeFixture(t)
	c := f.create(t)
	in, data := largeInput()
	upload, final, res := reserve(t, f, o, c, in, data)
	if !res.ReservationExpiresAt.Equal(c.ExpiresAt) || res.State != "uploading" {
		t.Fatal("reservation extended request TTL")
	}
	again, err := f.store.Reserve(ctx, c.ID, c.SubmissionToken, in)
	if err != nil || !again.ReservationExpiresAt.Equal(*res.ReservationExpiresAt) || again.Upload.URL != res.Upload.URL {
		t.Fatal("reservation retry changed identity/deadline", err)
	}
	status, err := f.store.Attempt(ctx, c.ID, c.SubmissionToken, 1, in.AttemptToken)
	if err != nil || status.State != "uploading" {
		t.Fatal(status, err)
	}
	owner, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
	if err != nil || owner.State != "uploading" {
		t.Fatal(owner, err)
	}
	_, err = f.store.Submit(ctx, c.ID, c.SubmissionToken, submission("text"))
	requireError(t, err, ErrConflict)
	_, err = f.store.Finalize(ctx, c.ID, c.RetrievalToken, attemptInput(in))
	requireError(t, err, ErrUnavailable)
	receipt, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in))
	if err != nil || receipt.State != "submitted" {
		t.Fatal(receipt, err)
	}
	o.write(upload, bytes.Repeat([]byte{2}, len(data)))
	again, err = f.store.Reserve(ctx, c.ID, c.SubmissionToken, in)
	if err != nil || again.State != "submitted" || again.Upload != nil {
		t.Fatal("accepted retry", again, err)
	}
	payload, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
	if err != nil || payload.Ciphertext != base64.StdEncoding.EncodeToString(data) {
		t.Fatal("open", err)
	}
	_, err = f.store.Open(ctx, c.ID, c.RetrievalToken)
	requireError(t, err, ErrConflict)
	_, err = f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in))
	if err != nil {
		t.Fatal("historical receipt lost", err)
	}
	jobs := outboxKeys(t, f.db)
	if len(jobs) != 2 || jobs[0].ObjectKey == jobs[1].ObjectKey {
		t.Fatal("staging/final cleanup not atomic", jobs)
	}
	if !bytes.Equal(o.body(final), data) {
		t.Fatal("API directly deleted accepted object")
	}
}

func TestLargeReservationGenerationFencesAndExhaustion(t *testing.T) {
	ctx := context.Background()
	f, o := largeFixture(t)
	f.store.opts.DefaultTTLSeconds = 3600
	c := f.create(t)
	in, data := largeInput()
	_, _, first := reserve(t, f, o, c, in, data)
	if !first.ReservationExpiresAt.Equal(f.now.Add(ReservationTTL)) {
		t.Fatal("pending TTL")
	}
	now := f.now.Add(ReservationTTL)
	f.store.now = func() time.Time { return now }
	instructions, err := f.store.Instructions(ctx, c.ID, c.SubmissionToken)
	if err != nil || instructions.Generation != 2 || !instructions.CanSubmit {
		t.Fatal(instructions, err)
	}
	second := in
	second.Generation = 2
	second.AttemptToken = attempt(7)
	reserve(t, f, o, c, second, data)
	abandoned, err := f.store.Abandon(ctx, c.ID, c.SubmissionToken, attemptInput(second))
	if err != nil || abandoned.Generation != 3 {
		t.Fatal(abandoned, err)
	}
	for _, stale := range []UploadInput{in, second} {
		_, err = f.store.Reserve(ctx, c.ID, c.SubmissionToken, stale)
		requireError(t, err, ErrUnavailable)
		_, err = f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(stale))
		requireError(t, err, ErrUnavailable)
	}
	for generation := 3; generation <= MaxGeneration; generation++ {
		next := in
		next.Generation = generation
		reserve(t, f, o, c, next, data)
		_, err = f.store.Abandon(ctx, c.ID, c.SubmissionToken, attemptInput(next))
		if generation == MaxGeneration {
			requireError(t, err, ErrUnavailable)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	_, err = f.store.Instructions(ctx, c.ID, c.SubmissionToken)
	requireError(t, err, ErrUnavailable)
	var generation int
	if err := f.db.QueryRow(`select generation from requests where id=?`, c.ID).Scan(&generation); err != nil || generation != 16 {
		t.Fatal(generation, err)
	}
}

func TestLargeValidationAndStorageFailureDoesNotAccept(t *testing.T) {
	for _, kind := range []string{"short", "long", "checksum", "missing", "put_response_lost"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f, o := largeFixture(t)
			c := f.create(t)
			in, data := largeInput()
			upload, final, _ := reserve(t, f, o, c, in, data)
			switch kind {
			case "short":
				o.write(upload, data[:len(data)-1])
			case "long":
				o.write(upload, append(data, 0))
			case "checksum":
				o.write(upload, bytes.Repeat([]byte{7}, len(data)))
			case "missing":
				o.Delete(ctx, upload)
			case "put_response_lost":
				o.putHook = func(string) error { return errors.New("response lost after write") }
			}
			_, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in))
			if err == nil {
				t.Fatal("bad upload accepted")
			}
			owner, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
			if err != nil || owner.State != "uploading" {
				t.Fatal(owner, err)
			}
			if count(t, f.db, "outbox_events") != 0 {
				t.Fatal("failed finalizer queued deletion")
			}
			if kind == "put_response_lost" {
				if !bytes.Equal(o.body(final), data) {
					t.Fatal("unknown PUT was deleted")
				}
				o.putHook = nil
				if _, err := f.store.ReconcileOnce(ctx, 10); err != nil {
					t.Fatal(err)
				}
				if count(t, f.db, "request_reconciliation_pending") != 0 {
					t.Fatal("reserved final key not protected")
				}
				if _, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLargeConcurrentFinalizersKeepWinnerKey(t *testing.T) {
	ctx := context.Background()
	f, o := largeFixture(t)
	c := f.create(t)
	in, data := largeInput()
	_, final, _ := reserve(t, f, o, c, in, data)
	other := reopenLarge(t, f)
	firstWritten := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	o.putHook = func(string) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(firstWritten)
			<-releaseFirst
			return errors.New("first PUT response lost")
		}
		return nil
	}
	first := make(chan error, 1)
	go func() { _, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in)); first <- err }()
	<-firstWritten
	if _, err := other.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in)); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	requireError(t, <-first, ErrStorage)
	if !bytes.Equal(o.body(final), data) {
		t.Fatal("losing finalizer erased winner")
	}
	for _, event := range outboxKeys(t, f.db) {
		if event.ObjectKey == final {
			t.Fatal("losing finalizer queued winner key")
		}
	}
	o.putHook = nil
	payload, err := other.Open(ctx, c.ID, c.RetrievalToken)
	if err != nil || payload.Ciphertext != base64.StdEncoding.EncodeToString(data) {
		t.Fatal(err)
	}
}

func TestLargeConcurrentFinalizeVersusRevokeAndExpiry(t *testing.T) {
	for _, kind := range []string{"revoke", "expiry", "abandon"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f, o := largeFixture(t)
			c := f.create(t)
			in, data := largeInput()
			_, final, _ := reserve(t, f, o, c, in, data)
			other := reopenLarge(t, f)
			written := make(chan struct{})
			release := make(chan struct{})
			o.putHook = func(string) error { close(written); <-release; return nil }
			done := make(chan error, 1)
			go func() { _, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in)); done <- err }()
			<-written
			switch kind {
			case "revoke":
				if err := other.Revoke(ctx, c.ID, c.RetrievalToken); err != nil {
					t.Fatal(err)
				}
			case "abandon":
				if _, err := other.Abandon(ctx, c.ID, c.SubmissionToken, attemptInput(in)); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				f.store.now = func() time.Time { return c.ExpiresAt }
			}
			close(release)
			if err := <-done; err == nil {
				t.Fatal("late finalizer accepted")
			}
			if !bytes.Equal(o.body(final), data) {
				t.Fatal("finalizer deleted key directly")
			}
			if kind == "revoke" {
				_, err := other.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in))
				requireError(t, err, ErrConflict)
			}
		})
	}
}

func TestLargeOpenDoesNotConsumeOnReadFailureOrLoseRevokeRace(t *testing.T) {
	for _, kind := range []string{"missing", "revoke", "parallel_open"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f, o := largeFixture(t)
			c := f.create(t)
			in, data := largeInput()
			_, final, _ := reserve(t, f, o, c, in, data)
			if _, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in)); err != nil {
				t.Fatal(err)
			}
			other := reopenLarge(t, f)
			if kind == "missing" {
				o.Delete(ctx, final)
				_, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
				requireError(t, err, ErrStorage)
				owner, err := other.Owner(ctx, c.ID, c.RetrievalToken)
				if err != nil || owner.State != "submitted" {
					t.Fatal(owner, err)
				}
				return
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			o.getHook = func(key string) {
				if key == final {
					block := false
					once.Do(func() { block = true; close(entered) })
					if block {
						<-release
					}
				}
			}
			done := make(chan error, 1)
			go func() { _, err := f.store.Open(ctx, c.ID, c.RetrievalToken); done <- err }()
			<-entered
			if kind == "revoke" {
				if err := other.Revoke(ctx, c.ID, c.RetrievalToken); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := other.Open(ctx, c.ID, c.RetrievalToken); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			requireError(t, <-done, ErrConflict)
		})
	}
}

func TestLargeBoundariesConflictAndDisabledRetrieval(t *testing.T) {
	ctx := context.Background()
	for _, size := range []int64{16, 17, 100, 101} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f, _ := largeFixture(t)
			c := f.create(t)
			in, _ := largeInput()
			in.SizeBytes = size
			_, err := f.store.Reserve(ctx, c.ID, c.SubmissionToken, in)
			switch size {
			case 16:
				requireError(t, err, ErrInvalid)
			case 101:
				requireError(t, err, ErrTooLarge)
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	f, o := largeFixture(t)
	c := f.create(t)
	in, data := largeInput()
	reserve(t, f, o, c, in, data)
	changed := in
	changed.AttemptToken = attempt(10)
	_, err := f.store.Reserve(ctx, c.ID, c.SubmissionToken, changed)
	requireError(t, err, ErrConflict)
	changed = in
	changed.CiphertextSHA256 = encoded(32, 10)
	_, err = f.store.Reserve(ctx, c.ID, c.SubmissionToken, changed)
	requireError(t, err, ErrConflict)
	if _, err := f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in)); err != nil {
		t.Fatal(err)
	}
	f.store.opts.Objects = nil
	_, err = f.store.Open(ctx, c.ID, c.RetrievalToken)
	requireError(t, err, ErrStorage)
	owner, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
	if err != nil || owner.State != "submitted" {
		t.Fatal("disabled object store consumed request", owner, err)
	}
}

func TestLargeReservationReaperInvalidatesGenerationAtomically(t *testing.T) {
	ctx := context.Background()
	f, o := largeFixture(t)
	f.store.opts.DefaultTTLSeconds = 3600
	c := f.create(t)
	in, data := largeInput()
	reserve(t, f, o, c, in, data)
	now := f.now.Add(ReservationTTL)
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := PurgeExpiredTx(ctx, tx, now, 1, f.store.opts.Outbox); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state string
	var generation int
	if err := f.db.QueryRow(`select state,generation from requests where id=?`, c.ID).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	if state != "waiting" || generation != 2 || count(t, f.db, "outbox_events") != 2 {
		t.Fatal("reservation expiry did not fence and enqueue both keys")
	}
	_, err = f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in))
	requireError(t, err, ErrUnavailable)
}

func TestLargeConcurrentReservationVersusInlineOrLarge(t *testing.T) {
	for _, competitor := range []string{"inline", "large"} {
		t.Run(competitor, func(t *testing.T) {
			ctx := context.Background()
			f, _ := largeFixture(t)
			c := f.create(t)
			in, _ := largeInput()
			other := reopenLarge(t, f)
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() { <-start; _, err := f.store.Reserve(ctx, c.ID, c.SubmissionToken, in); results <- err }()
			go func() {
				<-start
				var err error
				if competitor == "inline" {
					_, err = other.Submit(ctx, c.ID, c.SubmissionToken, submission("text"))
				} else {
					rival := in
					rival.AttemptToken = attempt(7)
					_, err = other.Reserve(ctx, c.ID, c.SubmissionToken, rival)
				}
				results <- err
			}()
			close(start)
			wins, conflicts := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					wins++
				} else if errors.Is(err, ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if wins != 1 || conflicts != 1 {
				t.Fatal("competing attempts both accepted", wins, conflicts)
			}
		})
	}
}
