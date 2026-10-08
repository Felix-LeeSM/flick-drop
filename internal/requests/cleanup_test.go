package requests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

func TestRequestReconciliationRestartsAndFindsLatePUTAfterMetadataPurge(t *testing.T) {
	ctx := context.Background()
	f, o := largeFixture(t)
	c := f.create(t)
	in, data := largeInput()
	upload, final, _ := reserve(t, f, o, c, in, data)
	o.write(final, data)
	if n, err := f.store.ReconcileOnce(ctx, 10); err != nil || n != 0 {
		t.Fatal("live keys not protected", n, err)
	}
	if err := f.store.Revoke(ctx, c.ID, c.RetrievalToken); err != nil {
		t.Fatal(err)
	}
	for _, event := range outboxKeys(t, f.db) {
		if err := o.Delete(ctx, event.ObjectKey); err != nil {
			t.Fatal(err)
		}
	}
	late := c.ExpiresAt.Add(24 * time.Hour)
	f.store.now = func() time.Time { return late }
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := PurgeExpiredTx(ctx, tx, late, 10, f.store.opts.Outbox); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if count(t, f.db, "requests") != 0 {
		t.Fatal("metadata retained")
	}
	// The old presigned PUT can finish well after metadata and signature expiry.
	o.write(upload, data)
	o.write(final, data)
	o.write("managed/secrets/other", data)
	if n, err := f.store.ReconcileOnce(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	restarted := reopenLarge(t, f)
	if n, err := restarted.ReconcileOnce(ctx, 1); err != nil || n != 1 {
		t.Fatal("cursor did not survive restart", n, err)
	}
	if count(t, f.db, "request_reconciliation_pending") != 2 {
		t.Fatal("missing per-key claims")
	}
	if n, err := restarted.ReconcileOnce(ctx, 10); err != nil || n != 0 {
		t.Fatal("duplicate pending jobs", n, err)
	}
	var previousJob string
	if err := f.db.QueryRow(`select job_id from request_reconciliation_pending where object_key=?`, upload).Scan(&previousJob); err != nil {
		t.Fatal(err)
	}
	if err := restarted.AcknowledgeObjectCleanup(ctx, previousJob, upload); err != nil {
		t.Fatal(err)
	}
	if n, err := restarted.ReconcileOnce(ctx, 10); err != nil || n != 1 {
		t.Fatal("terminal ACK did not allow a new job", n, err)
	}
	if err := restarted.AcknowledgeObjectCleanup(ctx, previousJob, upload); err != nil {
		t.Fatal(err)
	}
	var currentJob string
	if err := f.db.QueryRow(`select job_id from request_reconciliation_pending where object_key=?`, upload).Scan(&currentJob); err != nil || previousJob == currentJob {
		t.Fatal("stale ACK erased/reused claim", err)
	}
	for _, event := range outboxKeys(t, f.db) {
		if event.ObjectKey == "managed/secrets/other" {
			t.Fatal("request scan escaped namespace")
		}
	}
}

func TestRequestReconciliationFailsClosedAndRollsBack(t *testing.T) {
	for _, failure := range []string{"listing", "lookup", "outbox", "commit", "invalid_cursor", "stale_page"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			f, o := largeFixture(t)
			o.write(ObjectPrefix+"orphan", []byte{1})
			if _, err := f.db.Exec(`update request_reconciliation_cursor set continuation_token='prior'`); err != nil {
				t.Fatal(err)
			}
			o.listHook = func(prefix, cursor string, limit int) (storage.ObjectPage, error) {
				if prefix != ObjectPrefix || cursor != "prior" {
					t.Fatal("wrong listing scope or cursor")
				}
				if failure == "listing" {
					return storage.ObjectPage{}, errors.New("list denied")
				}
				if failure == "invalid_cursor" {
					return storage.ObjectPage{}, storage.ErrInvalidCursor
				}
				if failure == "stale_page" {
					if _, err := f.db.Exec(`update request_reconciliation_cursor set generation=generation+1, continuation_token='winner'`); err != nil {
						t.Fatal(err)
					}
				}
				return storage.ObjectPage{Keys: []string{ObjectPrefix + "orphan"}, NextCursor: "next"}, nil
			}
			switch failure {
			case "lookup":
				if _, err := f.db.Exec(`drop table requests`); err != nil {
					t.Fatal(err)
				}
			case "outbox":
				if _, err := f.db.Exec(`create trigger fail before insert on outbox_events begin select raise(abort,'enqueue failure'); end`); err != nil {
					t.Fatal(err)
				}
			case "commit":
				for _, sql := range []string{`create table deferred_failure(id text references requests(id) deferrable initially deferred)`, `create trigger fail after insert on request_reconciliation_pending begin insert into deferred_failure values('missing'); end`} {
					if _, err := f.db.Exec(sql); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := f.store.ReconcileOnce(ctx, 10)
			if failure == "stale_page" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failure treated as empty success")
			}
			if count(t, f.db, "request_reconciliation_pending") != 0 || count(t, f.db, "outbox_events") != 0 {
				t.Fatal("failed scan committed claim/outbox")
			}
			var cursor string
			if err := f.db.QueryRow(`select continuation_token from request_reconciliation_cursor`).Scan(&cursor); err != nil {
				t.Fatal(err)
			}
			want := "prior"
			if failure == "invalid_cursor" {
				want = ""
			}
			if failure == "stale_page" {
				want = "winner"
			}
			if cursor != want {
				t.Fatal("failed scan lost progress", cursor)
			}
		})
	}
}

func TestLargeTransitionsRollBackOutboxAndCommitFailures(t *testing.T) {
	for _, operation := range []string{"reserve", "finalize", "revoke", "abandon", "purge"} {
		for _, failure := range []string{"statement", "commit"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				ctx := context.Background()
				f, o := largeFixture(t)
				c := f.create(t)
				in, data := largeInput()
				if operation != "reserve" {
					reserve(t, f, o, c, in, data)
				}
				table := "outbox_events"
				if operation == "reserve" {
					table = "requests"
				}
				statement := `create trigger fail before insert on outbox_events begin select raise(abort,'failure'); end`
				if operation == "reserve" {
					statement = `create trigger fail before update of state on requests when new.state='uploading' begin select raise(abort,'failure'); end`
				}
				if failure == "commit" {
					if _, err := f.db.Exec(`create table deferred_failure(id text references requests(id) deferrable initially deferred)`); err != nil {
						t.Fatal(err)
					}
					action := "insert"
					if table == "requests" {
						action = "update of state"
					}
					statement = `create trigger fail after ` + action + ` on ` + table + ` begin insert into deferred_failure values('missing'); end`
				}
				if _, err := f.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
				var err error
				switch operation {
				case "reserve":
					_, err = f.store.Reserve(ctx, c.ID, c.SubmissionToken, in)
				case "finalize":
					_, err = f.store.Finalize(ctx, c.ID, c.SubmissionToken, attemptInput(in))
				case "revoke":
					err = f.store.Revoke(ctx, c.ID, c.RetrievalToken)
				case "abandon":
					_, err = f.store.Abandon(ctx, c.ID, c.SubmissionToken, attemptInput(in))
				case "purge":
					conn, e := f.db.Conn(ctx)
					if e != nil {
						t.Fatal(e)
					}
					defer conn.Close()
					transaction, e := conn.BeginTx(ctx, nil)
					if e != nil {
						t.Fatal(e)
					}
					tx := &writeTx{Tx: transaction, conn: conn}
					defer tx.close()
					err = PurgeExpiredTx(ctx, transaction, c.ExpiresAt, 10, f.store.opts.Outbox)
					if err == nil {
						err = f.store.commit(tx, time.Time{})
					}
					tx.close()
				}
				if err == nil {
					t.Fatal("injected transaction failure accepted")
				}
				var state string
				var generation int
				if err := f.db.QueryRow(`select state,generation from requests where id=?`, c.ID).Scan(&state, &generation); err != nil {
					t.Fatal(err)
				}
				want := "uploading"
				if operation == "reserve" {
					want = "waiting"
				}
				if state != want || generation != 1 || count(t, f.db, "outbox_events") != 0 {
					t.Fatal("transaction did not roll back", state, generation)
				}
			})
		}
	}
}
