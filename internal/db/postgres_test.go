package db

import (
	"context"
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
