package secrets

import (
	"database/sql"
	"testing"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

// NewStore pre-registers every kind/storage combination — inline plus each
// configured backend name — so flick_secret_created_total exists at 0 from
// process start; otherwise the series vanishes after every api restart until
// the next delivery and the usage dashboard reads "No data" (#374). The backend
// names are unique to this test so no other test's create can satisfy it.
func TestSecretCreatedChildrenPreRegistered(t *testing.T) {
	registry, err := storage.NewRegistry(
		storage.Backend{Name: "metrica", Store: newMockObjectStore(), MaxTotalBytes: 1},
		storage.Backend{Name: "metricb", Store: newMockObjectStore(), MaxTotalBytes: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(new(sql.DB), StoreOptions{PayloadInlineMaxBytes: 1, MaxObjectBytes: 1, MinTTLSeconds: 1, MaxTTLSeconds: 1, Objects: registry}); err != nil {
		t.Fatal(err)
	}
	mfs, err := telemetry.DefaultRegistry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	got := map[string]bool{}
	for _, mf := range mfs {
		if mf.GetName() != "flick_secret_created_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			got[labels["kind"]+"/"+labels["storage"]] = true
		}
	}
	for _, kind := range []string{KindText, KindFile} {
		for _, backend := range []string{StorageSQLite, "metrica", "metricb"} {
			if !got[kind+"/"+backend] {
				t.Fatalf("flick_secret_created_total missing pre-registered child %s/%s; got %v", kind, backend, got)
			}
		}
	}
}
