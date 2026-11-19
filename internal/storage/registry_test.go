package storage

import (
	"strings"
	"testing"
)

// stubStore satisfies ObjectStore without implementing anything: placement
// only resolves backends through the registry, never calling store methods.
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

// 무작위 값이 [0, n)의 각 구간에 떨어질 때 남은 용량 비례 구간이 그대로 선택된다.
func TestChooseWeightsByRemainingSpace(t *testing.T) {
	// Given: 남은 용량 oci 700, r2 100, minio 200 (합 1000)
	r := threeBackends(t)
	usage := map[string]int64{"oci": 300, "r2": 900, "minio": 800}

	cases := []struct {
		roll int64
		want string
	}{
		{0, "oci"}, {699, "oci"}, {700, "r2"}, {799, "r2"}, {800, "minio"}, {999, "minio"},
	}
	for _, c := range cases {
		// When: 무작위 값이 roll로 고정된다
		var gotN int64
		got := r.chooseWith(usage, 50, func(n int64) int64 { gotN = n; return c.roll })
		// Then: 구간 [0,700)은 oci, [700,800)은 r2, [800,1000)은 minio
		if gotN != 1000 {
			t.Fatalf("roll range = %d, want the summed free space 1000", gotN)
		}
		if got != c.want {
			t.Fatalf("roll %d: backend = %q, want %q", c.roll, got, c.want)
		}
	}
}

// 객체가 들어가지 않는 백엔드는 가중치 합에서도 빠진다.
func TestChooseSkipsBackendsWithoutRoom(t *testing.T) {
	// Given: r2는 남은 용량 100이라 200바이트 객체가 안 들어간다
	r := threeBackends(t)
	usage := map[string]int64{"oci": 500, "r2": 900, "minio": 800}

	// When: 가장 큰 무작위 값을 고른다
	var gotN int64
	got := r.chooseWith(usage, 200, func(n int64) int64 { gotN = n; return n - 1 })

	// Then: 후보는 oci(500)와 minio(200)뿐이고 마지막 구간은 minio다
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

// 백엔드가 하나면 상한 0은 무제한이다.
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
