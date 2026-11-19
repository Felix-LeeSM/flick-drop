package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// 스크랩할 때마다 DB 질의 결과를 그대로 게이지로 내보낸다. 라벨은 설정된 백엔드
// 이름뿐이다(키를 설정 이름으로 제한하는 일은 db.StorageObjectBytes가 맡는다).
func TestAPIStateCollectorReportsDatabaseValues(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(apiStateCollector{queries: APIStateQueries{
		PendingUploads: func(context.Context) (int, error) { return 3, nil },
		OutboxPending:  func(context.Context) (int, error) { return 7, nil },
		StorageBytesUsed: func(context.Context) (map[string]int64, error) {
			return map[string]int64{"oci": 4096, "r2": 0}, nil
		},
	}})

	want := `
# HELP flick_active_uploads Pending large-secret uploads awaiting /finalize, read from the flick_api database at scrape time.
# TYPE flick_active_uploads gauge
flick_active_uploads 3
# HELP flick_outbox_pending Outbox events waiting to be published, read from the flick_api database at scrape time.
# TYPE flick_outbox_pending gauge
flick_outbox_pending 7
# HELP flick_storage_bytes_used Object bytes per backend, aggregated from the flick_api database at scrape time.
# TYPE flick_storage_bytes_used gauge
flick_storage_bytes_used{storage="oci"} 4096
flick_storage_bytes_used{storage="r2"} 0
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

// 질의 하나가 실패하면 그 시계열만 빠지고 나머지와 /metrics 응답은 그대로 나간다.
func TestAPIStateCollectorDropsOnlyTheFailedSeries(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(apiStateCollector{queries: APIStateQueries{
		PendingUploads: func(context.Context) (int, error) { return 0, errors.New("database unavailable") },
		OutboxPending:  func(context.Context) (int, error) { return 2, nil },
		StorageBytesUsed: func(context.Context) (map[string]int64, error) {
			return nil, errors.New("database unavailable")
		},
	}})

	if got := testutil.CollectAndCount(registry, "flick_active_uploads", "flick_storage_bytes_used"); got != 0 {
		t.Fatalf("failed series emitted %d samples, want 0", got)
	}
	if got := testutil.CollectAndCount(registry, "flick_outbox_pending"); got != 1 {
		t.Fatalf("flick_outbox_pending samples = %d, want 1", got)
	}
}
