package secrets

import (
	"testing"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

// init() pre-registers every kind/storage combination so
// flick_secret_created_total exists at 0 from process start; otherwise the
// series vanishes after every api restart until the next delivery and the
// usage dashboard reads "No data" (#374).
func TestSecretCreatedChildrenPreRegistered(t *testing.T) {
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
		for _, backend := range []string{StorageSQLite, StorageS3, storage.SecondaryBackend} {
			if !got[kind+"/"+backend] {
				t.Fatalf("flick_secret_created_total missing pre-registered child %s/%s; got %v", kind, backend, got)
			}
		}
	}
}
