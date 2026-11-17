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

// 호스트 TZ가 UTC가 아니어도(예: 개발 맥의 KST) 스캔한 timestamptz는 UTC여야 한다.
// 저장소는 스캔 값을 그대로 JSON·HTTP 헤더로 내보내고, 그 계약은 "Z" 시각이다.
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

// Postgres에 아직 닿지 못하는 순간에 뜬 프로세스(NetworkPolicy가 새 파드를 몇 초 늦게
// 허용하거나 Postgres가 재시작 중)도 그 몇 초를 견디고 연결한다. 첫 거부에 바로 죽으면
// 롤아웃마다 파드가 한 번씩 재시작한다.
func TestOpenPostgresWaitsForAPostgresThatIsNotReachableYet(t *testing.T) {
	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set")
	}
	target, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}

	// Given: 아무도 듣지 않는 포트가 1.5초 뒤에야 실제 Postgres로 이어진다.
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

	// When
	conn, err := OpenPostgres(context.Background(), delayed.String())

	// Then: 첫 핑이 거부돼도 다시 시도해 연결한다.
	if err != nil {
		t.Fatalf("OpenPostgres before the listener appeared: %v", err)
	}
	_ = conn.Close()
}

// 끝내 닿지 않는 Postgres는 기다린 뒤 마지막 오류로 실패한다.
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
