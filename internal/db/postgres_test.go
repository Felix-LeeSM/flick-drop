package db

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestOpenPostgresScansTimestamptzAsUTC(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	previous := time.Local
	time.Local = seoul
	t.Cleanup(func() { time.Local = previous })

	conn := openTestAPIDatabase(t)
	want := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	var got time.Time
	if err := conn.QueryRowContext(context.Background(), `select $1::timestamptz`, want).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.Location() != time.UTC || !got.Equal(want) {
		t.Fatalf("scanned %s (%s), want %s in UTC", got, got.Location(), want)
	}
}

// Otherwise every rollout restarts each pod once while NetworkPolicy admits it.
func TestOpenPostgresWaitsForAPostgresThatIsNotReachableYet(t *testing.T) {
	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set")
	}
	target, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	delayedAddr := probe.Addr().String()
	_ = probe.Close()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		listener, err := net.Listen("tcp", delayedAddr)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = listener.Close() })
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				upstream, err := net.Dial("tcp", target.Host)
				if err != nil {
					return
				}
				defer upstream.Close()
				go func() { _, _ = io.Copy(upstream, client) }()
				_, _ = io.Copy(client, upstream)
			}()
		}
	}()
	delayed := *target
	delayed.Host = delayedAddr

	conn, err := OpenPostgres(context.Background(), delayed.String())

	if err != nil {
		t.Fatalf("OpenPostgres before the listener appeared: %v", err)
	}
	_ = conn.Close()
}

func TestOpenPostgresFailsOnceTheStartupWaitIsOver(t *testing.T) {
	previousWait, previousRetry := postgresStartupWait, postgresStartupRetry
	postgresStartupWait, postgresStartupRetry = 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { postgresStartupWait, postgresStartupRetry = previousWait, previousRetry })
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := probe.Addr().String()
	_ = probe.Close()

	started := time.Now()
	_, err = OpenPostgres(context.Background(), "postgres://flick:flick@"+closedAddr+"/flick?sslmode=disable&connect_timeout=1")

	if err == nil {
		t.Fatal("OpenPostgres succeeded against a closed port")
	}
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("OpenPostgres gave up after %s, want about the 300ms startup wait", elapsed)
	}
}
