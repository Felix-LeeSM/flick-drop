package requests

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

var testKeyOnce sync.Once
var testPublicKey string

func publicKey(t *testing.T) string {
	t.Helper()
	testKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		testPublicKey = base64.StdEncoding.EncodeToString(der)
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
	store, err := NewStore(conn, Options{PayloadInlineMaxBytes: 1024, MaxFileBytes: 900, MinTTLSeconds: 300, DefaultTTLSeconds: 600, MaxTTLSeconds: 3600})
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
func submission(kind string) SubmitInput {
	e := Envelope{Version: 1, Algorithm: algorithm, WrappedKey: encoded(256, 1), Nonce: encoded(12, 2)}
	if kind == "file" {
		e.EncryptedFilename = &EncryptedFilename{Nonce: encoded(12, 3), Ciphertext: encoded(17, 4)}
	}
	raw, _ := json.Marshal(e)
	return SubmitInput{Generation: 1, AttemptToken: attempt(5), Kind: kind, SizeBytes: 5, Envelope: raw, Ciphertext: encoded(21, 6)}
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
	der, _ := base64.StdEncoding.DecodeString(key)
	wantFP := sha256.Sum256(der)
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
			if err != nil || opened.Ciphertext != in.Ciphertext || opened.Kind != kind || opened.SizeBytes != in.SizeBytes {
				t.Fatalf("open: %v", err)
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
		func(v *SubmitInput) { v.Ciphertext = encoded(21, 7) },
		func(v *SubmitInput) { v.SizeBytes = 6; v.Ciphertext = encoded(22, 6) },
		func(v *SubmitInput) {
			v.Envelope = bytes.Replace(v.Envelope, []byte(encoded(256, 1)), []byte(encoded(256, 9)), 1)
		},
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
	if err != nil || opened.Ciphertext != in.Ciphertext {
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
						in.Ciphertext = encoded(21, byte(20+i))
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

func TestValidationRejectsMalformedCryptoAndBounds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, ttl := range []int{-1, 0, 299, 3601} {
		_, err := f.store.Create(ctx, publicKey(t), &ttl)
		requireError(t, err, ErrInvalid)
	}
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	wrongExponent := rsa.PublicKey{N: weak.N, E: 3}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	badKeys := []string{"", "not a key", publicKey(t) + "\n", publicKey(t) + "="}
	for _, key := range []any{&weak.PublicKey, &wrongExponent, &ec.PublicKey} {
		raw, _ := x509.MarshalPKIXPublicKey(key)
		badKeys = append(badKeys, base64.StdEncoding.EncodeToString(raw))
	}
	for _, key := range badKeys {
		_, err := f.store.Create(ctx, key, nil)
		requireError(t, err, ErrInvalid)
	}
	c := f.create(t)
	for name, change := range map[string]func(*SubmitInput){
		"algorithm": func(v *SubmitInput) {
			v.Envelope = bytes.Replace(v.Envelope, []byte(algorithm), []byte("RSA-OAEP-1"), 1)
		},
		"version": func(v *SubmitInput) {
			v.Envelope = bytes.Replace(v.Envelope, []byte(`"version":1`), []byte(`"version":2`), 1)
		},
		"unknown":   func(v *SubmitInput) { v.Envelope = append([]byte(`{"key":"forbidden",`), v.Envelope[1:]...) },
		"duplicate": func(v *SubmitInput) { v.Envelope = append([]byte(`{"version":1,`), v.Envelope[1:]...) },
		"equal nonces": func(v *SubmitInput) {
			v.Envelope = bytes.Replace(v.Envelope, []byte(encoded(12, 3)), []byte(encoded(12, 2)), 1)
		},
		"short wrapped key": func(v *SubmitInput) {
			v.Envelope = bytes.Replace(v.Envelope, []byte(encoded(256, 1)), []byte(encoded(255, 1)), 1)
		},
		"bad padding":      func(v *SubmitInput) { v.Ciphertext += "=" },
		"newline base64":   func(v *SubmitInput) { v.Ciphertext += "\n" },
		"wrong length":     func(v *SubmitInput) { v.SizeBytes++ },
		"missing filename": func(v *SubmitInput) { v.Envelope = submission("text").Envelope },
		"text filename":    func(v *SubmitInput) { v.Kind = "text" },
		"negative":         func(v *SubmitInput) { v.SizeBytes = -1 },
		"envelope size":    func(v *SubmitInput) { v.Envelope = append(bytes.Repeat([]byte(" "), 4096), v.Envelope...) },
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
		in.SizeBytes = 1024
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
