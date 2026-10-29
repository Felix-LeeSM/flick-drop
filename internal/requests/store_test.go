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
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

var testKeyOnce sync.Once
var testPublicKey string

func publicKey(t *testing.T) string {
	t.Helper()
	testKeyOnce.Do(func() {
		identity, err := age.GenerateHybridIdentity()
		if err != nil {
			t.Fatal(err)
		}
		testPublicKey = identity.Recipient().String()
	})
	return testPublicKey
}

type fixture struct {
	store *Store
	db    *sql.DB
	path  string
	now   time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.db")
	conn, err := db.OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.MigrateAPI(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(conn, Options{PayloadInlineMaxBytes: 1024, MaxObjectBytes: 900, MinTTLSeconds: 300, DefaultTTLSeconds: 600, MaxTTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 500000000, time.UTC)
	store.now = func() time.Time { return now }
	return fixture{store, conn, path, now}
}

func (f fixture) create(t *testing.T) Created {
	t.Helper()
	created, err := f.store.Create(context.Background(), publicKey(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func encoded(size int, value byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, size))
}
func attempt(value byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
}

// ageBody is an age-shaped body of size bytes: the store checks only the
// header magic, so the rest stands in for the encrypted payload.
func ageBody(size int, value byte) []byte {
	return append([]byte(AgeMagic), bytes.Repeat([]byte{value}, size-len(AgeMagic))...)
}
func ageEncoded(size int, value byte) string {
	return base64.StdEncoding.EncodeToString(ageBody(size, value))
}
func submission(kind string) SubmitInput {
	e := Envelope{Version: envelopeVersion}
	if kind == "file" {
		e.EncryptedFilename = ageEncoded(40, 4)
	}
	raw, _ := json.Marshal(e)
	return SubmitInput{Generation: 1, AttemptToken: attempt(5), Kind: kind, SizeBytes: 60, Envelope: raw, Ciphertext: ageEncoded(60, 6)}
}

// readOpened drains and closes an opened body, returning it as the base64 a
// submission carries.
func readOpened(t *testing.T, opened Opened) string {
	t.Helper()
	body, err := io.ReadAll(opened.Body)
	if err != nil || opened.Body.Close() != nil || int64(len(body)) != opened.SizeBytes {
		t.Fatalf("opened body: %v", err)
	}
	return base64.StdEncoding.EncodeToString(body)
}

func requireError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}
func count(t *testing.T, conn *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow("select count(*) from " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateCapabilitiesAndAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.create(t)
	if len(c.SubmissionToken) != 43 || len(c.RetrievalToken) != 43 || c.SubmissionToken == c.RetrievalToken || !c.ExpiresAt.Equal(f.now.Add(600*time.Second)) {
		t.Fatal("capability or TTL contract")
	}
	var sh, rh []byte
	var key, fp string
	if err := f.db.QueryRow(`select submission_token_hash, retrieval_token_hash, public_key, fingerprint from requests where id = ?`, c.ID).Scan(&sh, &rh, &key, &fp); err != nil {
		t.Fatal(err)
	}
	wantSH, _ := tokenHash(c.SubmissionToken)
	wantRH, _ := tokenHash(c.RetrievalToken)
	wantFP := sha256.Sum256([]byte(key))
	if !bytes.Equal(sh, wantSH) || !bytes.Equal(rh, wantRH) || fp != base64.RawURLEncoding.EncodeToString(wantFP[:]) {
		t.Fatal("stored hashes or fingerprint")
	}
	for _, token := range []string{"", "bad", c.RetrievalToken, publicKey(t), c.SubmissionToken + "="} {
		_, err := f.store.Instructions(ctx, c.ID, token)
		requireError(t, err, ErrUnavailable)
	}
	for _, token := range []string{"", "bad", c.SubmissionToken, publicKey(t)} {
		_, err := f.store.Owner(ctx, c.ID, token)
		requireError(t, err, ErrUnavailable)
		_, err = f.store.Open(ctx, c.ID, token)
		requireError(t, err, ErrUnavailable)
		requireError(t, f.store.Revoke(ctx, c.ID, token), ErrUnavailable)
	}
	_, err := f.store.Instructions(ctx, "missing", c.SubmissionToken)
	requireError(t, err, ErrUnavailable)
	instructions, err := f.store.Instructions(ctx, c.ID, c.SubmissionToken)
	if err != nil || !instructions.CanSubmit || instructions.Generation != 1 {
		t.Fatalf("instructions: %v", err)
	}
	owner, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
	if err != nil || owner.State != "waiting" {
		t.Fatalf("owner: %v", err)
	}
	_, err = f.store.Open(ctx, c.ID, c.RetrievalToken)
	requireError(t, err, ErrConflict)
}

func TestInlineLifecycleAndReceiptAfterConsumption(t *testing.T) {
	for _, kind := range []string{"text", "file"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			c := f.create(t)
			ctx := context.Background()
			in := submission(kind)
			waiting, err := f.store.Attempt(ctx, c.ID, c.SubmissionToken, 1, in.AttemptToken)
			if err != nil || waiting.State != "waiting" {
				t.Fatalf("waiting: %v", err)
			}
			if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
				t.Fatal(err)
			}
			// JSON whitespace is not immutable content; no new transition or TTL.
			in.Envelope = append([]byte("\n "), in.Envelope...)
			if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				status, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
				if err != nil || status.State != "submitted" || !status.ExpiresAt.Equal(c.ExpiresAt) {
					t.Fatalf("status: %v", err)
				}
			}
			if count(t, f.db, "request_payloads") != 1 {
				t.Fatal("status/duplicate consumed payload")
			}
			opened, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
			if err != nil || opened.Kind != kind || opened.SizeBytes != in.SizeBytes || opened.ObjectKey != "" || readOpened(t, opened) != in.Ciphertext {
				t.Fatalf("open: %v", err)
			}
			wantEnvelope := Envelope{}
			_ = json.Unmarshal(submission(kind).Envelope, &wantEnvelope)
			if opened.Envelope != wantEnvelope {
				t.Fatal("opened envelope")
			}
			if count(t, f.db, "request_payloads") != 0 {
				t.Fatal("payload retained after open")
			}
			var envelope sql.NullString
			if err := f.db.QueryRow(`select envelope_json from requests where id=?`, c.ID).Scan(&envelope); err != nil || envelope.Valid {
				t.Fatal("envelope retained")
			}
			_, err = f.store.Open(ctx, c.ID, c.RetrievalToken)
			requireError(t, err, ErrConflict)
			requireError(t, f.store.Revoke(ctx, c.ID, c.RetrievalToken), ErrConflict)
			if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
				t.Fatalf("accepted retry after open: %v", err)
			}
			receipt, err := f.store.Attempt(ctx, c.ID, c.SubmissionToken, 1, in.AttemptToken)
			if err != nil || receipt.State != "accepted" {
				t.Fatalf("accepted receipt: %v", err)
			}
			if count(t, f.db, "outbox_events") != 0 {
				t.Fatal("inline payload entered worker contract")
			}
		})
	}
}

func TestImmutableSubmissionAndGeneration(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	ctx := context.Background()
	in := submission("text")
	if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SubmitInput){
		func(v *SubmitInput) { v.AttemptToken = attempt(7) },
		func(v *SubmitInput) { v.Ciphertext = ageEncoded(60, 7) },
		func(v *SubmitInput) { v.SizeBytes = 61; v.Ciphertext = ageEncoded(61, 6) },
		func(v *SubmitInput) { *v = submission("file") },
	} {
		changed := in
		change(&changed)
		_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, changed)
		requireError(t, err, ErrConflict)
	}
	for _, generation := range []int{0, 2, 16, 17} {
		changed := in
		changed.Generation = generation
		want := ErrUnavailable
		if generation == 0 || generation == 17 {
			want = ErrInvalid
		}
		_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, changed)
		requireError(t, err, want)
		_, err = f.store.Attempt(ctx, c.ID, c.SubmissionToken, generation, in.AttemptToken)
		requireError(t, err, want)
	}
	lost, err := f.store.Attempt(ctx, c.ID, c.SubmissionToken, 1, attempt(8))
	if err != nil || lost.State != "unavailable" {
		t.Fatalf("losing attempt: %v", err)
	}
	opened, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
	if err != nil || readOpened(t, opened) != in.Ciphertext {
		t.Fatalf("accepted content replaced: %v", err)
	}
}

func TestConcurrentSubmissionAndOpenAcrossConnections(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "different_attempts", true: "same_attempt"}[same], func(t *testing.T) {
			f := newFixture(t)
			c := f.create(t)
			ctx := context.Background()
			conn, err := db.OpenSQLite(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			second, err := NewStore(conn, f.store.opts)
			if err != nil {
				t.Fatal(err)
			}
			second.now = f.store.now
			stores := []*Store{f.store, second}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, store := range stores {
				go func(i int, s *Store) {
					<-start
					in := submission("text")
					if !same {
						in.AttemptToken = attempt(byte(20 + i))
						in.Ciphertext = ageEncoded(60, byte(20+i))
					}
					_, err := s.Submit(ctx, c.ID, c.SubmissionToken, in)
					results <- err
				}(i, store)
			}
			close(start)
			success := 0
			for range stores {
				err := <-results
				if err == nil {
					success++
				} else {
					requireError(t, err, ErrConflict)
				}
			}
			want := 1
			if same {
				want = 2
			}
			if success != want || count(t, f.db, "request_payloads") != 1 {
				t.Fatalf("submission successes %d, want %d", success, want)
			}
			start = make(chan struct{})
			for _, store := range stores {
				go func(s *Store) { <-start; _, err := s.Open(ctx, c.ID, c.RetrievalToken); results <- err }(store)
			}
			close(start)
			success = 0
			for range stores {
				err := <-results
				if err == nil {
					success++
				} else {
					requireError(t, err, ErrConflict)
				}
			}
			if success != 1 || count(t, f.db, "request_payloads") != 0 {
				t.Fatalf("open successes %d", success)
			}
		})
	}
}

func TestRevokeAndOpenRace(t *testing.T) {
	for _, submit := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "submitted"}[submit], func(t *testing.T) {
			f := newFixture(t)
			c := f.create(t)
			ctx := context.Background()
			in := submission("file")
			if submit {
				if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.Revoke(ctx, c.ID, c.RetrievalToken); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Revoke(ctx, c.ID, c.RetrievalToken); err != nil {
				t.Fatal(err)
			}
			status, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
			if err != nil || status.State != "cancelled" {
				t.Fatalf("revoke: %v", err)
			}
			_, err = f.store.Open(ctx, c.ID, c.RetrievalToken)
			requireError(t, err, ErrConflict)
			in.AttemptToken = attempt(9)
			_, err = f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
			requireError(t, err, ErrConflict)
			if count(t, f.db, "request_payloads") != 0 {
				t.Fatal("revoked bytes remain")
			}
		})
	}
	f := newFixture(t)
	ctx := context.Background()
	c := f.create(t)
	if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, submission("text")); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := f.store.Open(ctx, c.ID, c.RetrievalToken); results <- err }()
	go func() { <-start; results <- f.store.Revoke(ctx, c.ID, c.RetrievalToken) }()
	close(start)
	wins := 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else {
			requireError(t, err, ErrConflict)
		}
	}
	if wins != 1 {
		t.Fatalf("open/revoke winners %d", wins)
	}
}

func TestExpiryPurgeAndOriginalDeadline(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, state := range []string{"waiting", "submitted", "consumed", "cancelled"} {
		c := f.create(t)
		in := submission("text")
		if state != "waiting" {
			if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
				t.Fatal(err)
			}
		}
		if state == "consumed" {
			if _, err := f.store.Open(ctx, c.ID, c.RetrievalToken); err != nil {
				t.Fatal(err)
			}
		}
		if state == "cancelled" {
			if err := f.store.Revoke(ctx, c.ID, c.RetrievalToken); err != nil {
				t.Fatal(err)
			}
		}
		f.store.now = func() time.Time { return c.ExpiresAt.Add(-time.Nanosecond) }
		if _, err := f.store.Owner(ctx, c.ID, c.RetrievalToken); err != nil {
			t.Fatal(err)
		}
		f.store.now = func() time.Time { return c.ExpiresAt }
		_, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
		requireError(t, err, ErrUnavailable)
		_, err = f.store.Instructions(ctx, c.ID, c.SubmissionToken)
		requireError(t, err, ErrUnavailable)
		_, err = f.store.Attempt(ctx, c.ID, c.SubmissionToken, 1, in.AttemptToken)
		requireError(t, err, ErrUnavailable)
		_, err = f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
		requireError(t, err, ErrUnavailable)
		_, err = f.store.Open(ctx, c.ID, c.RetrievalToken)
		requireError(t, err, ErrUnavailable)
		requireError(t, f.store.Revoke(ctx, c.ID, c.RetrievalToken), ErrUnavailable)
		f.store.now = func() time.Time { return f.now }
	}
	for i := 0; i < 2; i++ {
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := PurgeExpiredTx(ctx, tx, f.now.Add(600*time.Second), 2); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got := count(t, f.db, "requests"); got != 2-i*2 {
			t.Fatalf("bounded purge count %d", got)
		}
	}
	if count(t, f.db, "request_payloads") != 0 {
		t.Fatal("cascade left ciphertext")
	}
}

func TestTransactionRollbackAndExpiryAtCommit(t *testing.T) {
	for _, operation := range []string{"submit", "open", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			c := f.create(t)
			in := submission("file")
			if operation != "submit" {
				if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
					t.Fatal(err)
				}
			}
			trigger := `create trigger fail before insert on request_payloads begin select raise(abort,'storage failure'); end`
			if operation != "submit" {
				trigger = `create trigger fail before update of state on requests when new.state in ('consumed','cancelled') begin select raise(abort,'storage failure'); end`
			}
			if _, err := f.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			perform := func() error {
				switch operation {
				case "submit":
					_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
					return err
				case "open":
					_, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
					return err
				default:
					return f.store.Revoke(ctx, c.ID, c.RetrievalToken)
				}
			}
			if perform() == nil {
				t.Fatal("failed transaction succeeded")
			}
			if _, err := f.db.Exec(`drop trigger fail`); err != nil {
				t.Fatal(err)
			}
			status, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
			want := "submitted"
			payloads := 1
			if operation == "submit" {
				want = "waiting"
				payloads = 0
			}
			if err != nil || status.State != want || count(t, f.db, "request_payloads") != payloads {
				t.Fatal("partial transaction")
			}
			calls := 0
			f.store.now = func() time.Time {
				calls++
				if calls >= 2 {
					return c.ExpiresAt
				}
				return f.now
			}
			requireError(t, perform(), ErrUnavailable)
			f.store.now = func() time.Time { return f.now }
			if count(t, f.db, "request_payloads") != payloads {
				t.Fatal("expiry rollback lost bytes")
			}
			if err := perform(); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
		})
	}
}

func TestFailedCommitRollsBackBeforeConnectionReuse(t *testing.T) {
	for _, operation := range []string{"submit", "open", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			c := f.create(t)
			in := submission("text")
			if operation != "submit" {
				if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.Exec(`create table fail_commit (request_id text references requests(id) deferrable initially deferred)`); err != nil {
				t.Fatal(err)
			}
			target := map[string]string{"submit": "submitted", "open": "consumed", "revoke": "cancelled"}[operation]
			if _, err := f.db.Exec(`create trigger fail_transition after update of state on requests when new.state = '` + target + `' and old.state != new.state begin insert into fail_commit values ('missing-parent'); end`); err != nil {
				t.Fatal(err)
			}
			perform := func() error {
				switch operation {
				case "submit":
					_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
					return err
				case "open":
					_, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
					return err
				default:
					return f.store.Revoke(ctx, c.ID, c.RetrievalToken)
				}
			}
			if perform() == nil {
				t.Fatal("deferred constraint must fail at COMMIT")
			}
			status, err := f.store.Owner(ctx, c.ID, c.RetrievalToken)
			want := "submitted"
			payloads := 1
			if operation == "submit" {
				want = "waiting"
				payloads = 0
			}
			if err != nil || status.State != want || count(t, f.db, "request_payloads") != payloads || count(t, f.db, "fail_commit") != 0 {
				t.Fatal("failed COMMIT leaked partial state")
			}
			if _, err := f.db.Exec(`drop trigger fail_transition`); err != nil {
				t.Fatal(err)
			}
			if err := perform(); err != nil {
				t.Fatalf("retry after failed COMMIT: %v", err)
			}
		})
	}
}

func TestCancelledCommitPreservesReplacementCleanup(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	f.store.now = func() time.Time {
		calls++
		if calls == 2 {
			cancel()
			// Let database/sql perform its asynchronous cancellation rollback.
			// Our failed-COMMIT cleanup then discards that physical connection.
			time.Sleep(10 * time.Millisecond)
		}
		return f.now
	}
	in := submission("text")
	_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("cancelled submission: %v", err)
	}
	f.store.now = func() time.Time { return f.now }
	for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := f.db.QueryRow("pragma " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("replacement %s = %d, want %d: %v", pragma, got, want, err)
		}
	}
	if count(t, f.db, "request_payloads") != 0 {
		t.Fatal("cancelled transaction retained a payload")
	}
	if _, err := f.store.Submit(context.Background(), c.ID, c.SubmissionToken, in); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := PurgeExpiredTx(context.Background(), tx, c.ExpiresAt, 10); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if count(t, f.db, "requests") != 0 || count(t, f.db, "request_payloads") != 0 {
		t.Fatal("replacement connection left expired metadata or orphan ciphertext")
	}
}

func TestValidationRejectsMalformedCryptoAndBounds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, ttl := range []int{-1, 0, 299, 3601} {
		_, err := f.store.Create(ctx, publicKey(t), &ttl)
		requireError(t, err, ErrInvalid)
	}
	classic, _ := age.GenerateX25519Identity()
	badKeys := []string{"", "not a key", publicKey(t) + "\n", publicKey(t) + "=", " " + publicKey(t), strings.ToUpper(publicKey(t)), publicKey(t)[:len(publicKey(t))-1], classic.Recipient().String(), publicKey(t) + strings.Repeat("q", maxRecipientChars)}
	for _, key := range badKeys {
		_, err := f.store.Create(ctx, key, nil)
		requireError(t, err, ErrInvalid)
	}
	c := f.create(t)
	for name, change := range map[string]func(*SubmitInput){
		"version": func(v *SubmitInput) {
			v.Envelope = bytes.Replace(v.Envelope, []byte(`"version":2`), []byte(`"version":1`), 1)
		},
		"unknown":   func(v *SubmitInput) { v.Envelope = append([]byte(`{"key":"forbidden",`), v.Envelope[1:]...) },
		"duplicate": func(v *SubmitInput) { v.Envelope = append([]byte(`{"version":2,`), v.Envelope[1:]...) },
		"filename magic": func(v *SubmitInput) {
			v.Envelope = []byte(`{"version":2,"encrypted_filename":"` + encoded(40, 4) + `"}`)
		},
		"filename padding": func(v *SubmitInput) {
			v.Envelope = []byte(`{"version":2,"encrypted_filename":"` + ageEncoded(40, 4) + `="}`)
		},
		"filename size": func(v *SubmitInput) {
			v.Envelope = []byte(`{"version":2,"encrypted_filename":"` + ageEncoded(maxEncryptedNameLen+1, 4) + `"}`)
		},
		"bad padding":      func(v *SubmitInput) { v.Ciphertext += "=" },
		"newline base64":   func(v *SubmitInput) { v.Ciphertext += "\n" },
		"wrong length":     func(v *SubmitInput) { v.SizeBytes++ },
		"body magic":       func(v *SubmitInput) { v.Ciphertext = encoded(60, 6) },
		"missing filename": func(v *SubmitInput) { v.Envelope = submission("text").Envelope },
		"text filename":    func(v *SubmitInput) { v.Kind = "text" },
		"negative":         func(v *SubmitInput) { v.SizeBytes = -1 },
		"envelope size":    func(v *SubmitInput) { v.Envelope = append(bytes.Repeat([]byte(" "), EnvelopeMaxBytes), v.Envelope...) },
		"bad attempt":      func(v *SubmitInput) { v.AttemptToken += "=" },
	} {
		t.Run(name, func(t *testing.T) {
			in := submission("file")
			change(&in)
			_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
			requireError(t, err, ErrInvalid)
		})
	}
	for _, kind := range []string{"text", "file"} {
		in := submission(kind)
		in.SizeBytes = 1025
		_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
		requireError(t, err, ErrTooLarge)
	}
	in := submission("file")
	in.SizeBytes = 901
	_, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in)
	requireError(t, err, ErrTooLarge)
	if count(t, f.db, "request_payloads") != 0 {
		t.Fatal("invalid input wrote payload")
	}
}

func TestInlineExactLimits(t *testing.T) {
	for _, tc := range []struct {
		kind string
		size int64
	}{{"text", int64(len(AgeMagic))}, {"file", int64(len(AgeMagic))}, {"text", 1024}, {"file", 900}} {
		t.Run(fmt.Sprintf("%s_%d", tc.kind, tc.size), func(t *testing.T) {
			f := newFixture(t)
			c := f.create(t)
			in := submission(tc.kind)
			in.SizeBytes = tc.size
			in.Ciphertext = ageEncoded(int(tc.size), 7)
			if _, err := f.store.Submit(context.Background(), c.ID, c.SubmissionToken, in); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.Open(context.Background(), c.ID, c.RetrievalToken)
			if err != nil || readOpened(t, got) != in.Ciphertext {
				t.Fatalf("inline boundary round trip: %v", err)
			}
		})
	}
}
