package storage

import (
	"strings"
	"testing"
)

type stubStore struct{ ObjectStore }

func threeBackends(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry(
		Backend{Name: "oci", Store: stubStore{}, MaxTotalBytes: 1000},
		Backend{Name: "r2", Store: stubStore{}, MaxTotalBytes: 1000},
		Backend{Name: "minio", Store: stubStore{}, MaxTotalBytes: 1000},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChooseWeightsByRemainingSpace(t *testing.T) {
	r := threeBackends(t)
	usage := map[string]int64{"oci": 300, "r2": 900, "minio": 800}

	cases := []struct {
		roll int64
		want string
	}{
		{0, "oci"}, {699, "oci"}, {700, "r2"}, {799, "r2"}, {800, "minio"}, {999, "minio"},
	}
	for _, c := range cases {
		var gotN int64
		got := r.chooseWith(usage, 50, func(n int64) int64 { gotN = n; return c.roll })
		if gotN != 1000 {
			t.Fatalf("roll range = %d, want the summed free space 1000", gotN)
		}
		if got != c.want {
			t.Fatalf("roll %d: backend = %q, want %q", c.roll, got, c.want)
		}
	}
}

func TestChooseSkipsBackendsWithoutRoom(t *testing.T) {
	r := threeBackends(t)
	usage := map[string]int64{"oci": 500, "r2": 900, "minio": 800}

	var gotN int64
	got := r.chooseWith(usage, 200, func(n int64) int64 { gotN = n; return n - 1 })

	if gotN != 700 || got != "minio" {
		t.Fatalf("range %d, backend %q; want 700 and minio", gotN, got)
	}
}

func TestChooseReportsFull(t *testing.T) {
	r := threeBackends(t)
	usage := map[string]int64{"oci": 1000, "r2": 999, "minio": 1000}

	if got := r.Choose(usage, 2); got != "" {
		t.Fatalf("backend = %q, want empty when no backend fits the object", got)
	}
}

func TestChooseSingleBackendZeroCeilingIsUnlimited(t *testing.T) {
	r, err := NewRegistry(Backend{Name: "minio", Store: stubStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Choose(map[string]int64{"minio": 1 << 40}, 1<<30); got != "minio" {
		t.Fatalf("backend = %q, want minio when its ceiling is zero (unlimited)", got)
	}
}

func TestChooseSingleBackendRespectsCeiling(t *testing.T) {
	r, err := NewRegistry(Backend{Name: "minio", Store: stubStore{}, MaxTotalBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Choose(map[string]int64{"minio": 90}, 11); got != "" {
		t.Fatalf("backend = %q, want empty past the only backend's ceiling", got)
	}
}

func TestNewRegistryRejectsBadConfigurations(t *testing.T) {
	cases := map[string][]Backend{
		"none":                       nil,
		"upper-case name":            {{Name: "OCI", Store: stubStore{}}},
		"underscore name":            {{Name: "s3_object", Store: stubStore{}}},
		"duplicate name":             {{Name: "oci", Store: stubStore{}, MaxTotalBytes: 1}, {Name: "oci", Store: stubStore{}, MaxTotalBytes: 1}},
		"missing store":              {{Name: "oci"}},
		"uncapped among several":     {{Name: "oci", Store: stubStore{}, MaxTotalBytes: 1}, {Name: "r2", Store: stubStore{}}},
		"negative ceiling":           {{Name: "oci", Store: stubStore{}, MaxTotalBytes: -1}},
		"name longer than 16 chars":  {{Name: strings.Repeat("a", 17), Store: stubStore{}}},
		"name starting with a digit": {{Name: "2r", Store: stubStore{}}},
	}
	for name, backends := range cases {
		if _, err := NewRegistry(backends...); err == nil {
			t.Errorf("%s: NewRegistry accepted %+v", name, backends)
		}
	}
}

func TestForResolvesOnlyConfiguredNames(t *testing.T) {
	r := threeBackends(t)
	if _, err := r.For("r2"); err != nil {
		t.Fatalf("For(r2): %v", err)
	}
	for _, name := range []string{"", "s3_object", "sqlite_blob", "gcs"} {
		if _, err := r.For(name); err == nil {
			t.Errorf("For(%q) resolved an unconfigured backend", name)
		}
	}
	if got := strings.Join(r.Names(), ","); got != "oci,r2,minio" {
		t.Fatalf("Names = %s, want configuration order", got)
	}
}
