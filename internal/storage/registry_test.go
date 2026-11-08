package storage

import "testing"

// stubStore satisfies ObjectStore without implementing anything: ChooseBackend
// only resolves backends through the registry, never calling store methods.
type stubStore struct{ ObjectStore }

func TestChooseBackendPrefersPrimaryWhileItFits(t *testing.T) {
	r, err := NewRegistry(stubStore{}, stubStore{})
	if err != nil {
		t.Fatal(err)
	}

	backend := ChooseBackend(r,
		map[string]int64{PrimaryBackend: 100, SecondaryBackend: 900},
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 1000},
		500)
	if backend != PrimaryBackend {
		t.Fatalf("backend = %q, want primary while the primary has headroom", backend)
	}
}

func TestChooseBackendSpillsToSecondary(t *testing.T) {
	r, err := NewRegistry(stubStore{}, stubStore{})
	if err != nil {
		t.Fatal(err)
	}

	// The primary is at ceiling; the next object lands on the secondary even
	// though the secondary is already more loaded.
	backend := ChooseBackend(r,
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 900},
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 1000},
		1)
	if backend != SecondaryBackend {
		t.Fatalf("backend = %q, want secondary once the primary is at ceiling", backend)
	}
}

func TestChooseBackendBoundaryAcceptsExactFit(t *testing.T) {
	r, err := NewRegistry(stubStore{}, stubStore{})
	if err != nil {
		t.Fatal(err)
	}

	// usage+size == ceiling still fits; strictly greater does not.
	backend := ChooseBackend(r,
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 1000},
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 1000},
		0)
	if backend != PrimaryBackend {
		t.Fatalf("backend = %q, want primary at an exact-fit boundary", backend)
	}
}

func TestChooseBackendZeroCeilingMeansUnlimited(t *testing.T) {
	r, err := NewRegistry(stubStore{}, stubStore{})
	if err != nil {
		t.Fatal(err)
	}

	backend := ChooseBackend(r,
		map[string]int64{PrimaryBackend: 1 << 40},
		map[string]int64{PrimaryBackend: 0},
		1<<30)
	if backend != PrimaryBackend {
		t.Fatalf("backend = %q, want primary when its ceiling is zero (unlimited)", backend)
	}
}

func TestChooseBackendSkipsUnconfiguredSecondary(t *testing.T) {
	r, err := NewRegistry(stubStore{}, nil) // primary only
	if err != nil {
		t.Fatal(err)
	}

	backend := ChooseBackend(r,
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 0},
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 1000},
		1)
	if backend != "" {
		t.Fatalf("backend = %q, want empty when only the primary is configured and full", backend)
	}
}

func TestChooseBackendReportsFull(t *testing.T) {
	r, err := NewRegistry(stubStore{}, stubStore{})
	if err != nil {
		t.Fatal(err)
	}

	backend := ChooseBackend(r,
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 1000},
		map[string]int64{PrimaryBackend: 1000, SecondaryBackend: 1000},
		1)
	if backend != "" {
		t.Fatalf("backend = %q, want empty when every backend is at ceiling", backend)
	}
}
