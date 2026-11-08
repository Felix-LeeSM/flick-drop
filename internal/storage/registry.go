package storage

import "fmt"

// Backend identifiers stored in the secrets/requests storage_backend columns.
// 's3_object' is the primary backend and owns every row written before
// multi-backend support; 'r2_object' marks objects placed on the secondary
// backend. Empty values resolve to the primary.
const (
	PrimaryBackend   = "s3_object"
	SecondaryBackend = "r2_object"
)

// Registry holds the named object-store backends. A registry is built at
// process wiring from each enabled backend's own configuration; zero entries
// mean object storage is disabled entirely. It is read-only after construction
// and safe for concurrent use.
type Registry struct {
	primary   ObjectStore
	secondary ObjectStore // nil unless a secondary backend is configured
}

// NewRegistry wraps the primary backend (required) and the optional secondary.
// Primary may be nil only when every backend is disabled, which callers
// represent by passing no registry at all.
func NewRegistry(primary ObjectStore, secondary ObjectStore) (*Registry, error) {
	if primary == nil {
		return nil, fmt.Errorf("primary object backend is required")
	}
	return &Registry{primary: primary, secondary: secondary}, nil
}

// For resolves the backend a row was written to. Empty backend means a row or
// job predating multi-backend support and resolves to the primary. Unknown
// backends are an error so callers never silently delete from the wrong
// bucket.
func (r *Registry) For(backend string) (ObjectStore, error) {
	switch backend {
	case "", PrimaryBackend:
		return r.primary, nil
	case SecondaryBackend:
		if r.secondary == nil {
			return nil, fmt.Errorf("backend %q is not configured", SecondaryBackend)
		}
		return r.secondary, nil
	default:
		return nil, fmt.Errorf("unknown object backend %q", backend)
	}
}

// Each visits every configured backend, primary first. The visit stops on the
// first error.
func (r *Registry) Each(fn func(backend string, store ObjectStore) error) error {
	if err := fn(PrimaryBackend, r.primary); err != nil {
		return err
	}
	if r.secondary != nil {
		if err := fn(SecondaryBackend, r.secondary); err != nil {
			return err
		}
	}
	return nil
}

// ForRequest resolves a backend supporting server-side final writes (request
// finalize's PutStream). Every built-in backend implements it; the check keeps
// a future read-only backend from failing finalize deep inside a transaction.
func (r *Registry) ForRequest(backend string) (RequestObjectStore, error) {
	store, err := r.For(backend)
	if err != nil {
		return nil, err
	}
	req, ok := store.(RequestObjectStore)
	if !ok {
		return nil, fmt.Errorf("backend %q does not support server-side final writes", backend)
	}
	return req, nil
}

// Primary returns the primary backend's store without resolving by name.
func (r *Registry) Primary() ObjectStore { return r.primary }

// ChooseBackend picks the placement backend for a new object: the primary
// first, then the secondary for whatever the primary's ceiling cannot fit.
// usage maps backend -> bytes already plausibly in the bucket (see db's
// StorageObjectBytes); ceilings maps backend -> the cumulative byte cap, where
// a zero or missing entry means unlimited. Backends the registry cannot
// resolve are skipped. It returns "" when no configured backend has room.
func ChooseBackend(r *Registry, usage, ceilings map[string]int64, size int64) string {
	for _, backend := range []string{PrimaryBackend, SecondaryBackend} {
		if _, err := r.For(backend); err != nil {
			continue
		}
		if ceiling := ceilings[backend]; ceiling == 0 || usage[backend]+size <= ceiling {
			return backend
		}
	}
	return ""
}

// Len reports how many backends are configured (1 or 2 for a non-empty
// registry).
func (r *Registry) Len() int {
	if r.secondary == nil {
		return 1
	}
	return 2
}
