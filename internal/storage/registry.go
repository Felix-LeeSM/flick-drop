package storage

import (
	"fmt"
	"math/rand/v2"
	"regexp"
)

// backendNamePattern is the shape of an operator-chosen backend name. The name
// is stored in the secrets/requests storage_backend columns and in delete
// events, and its upper-case form names the backend's environment variables
// (FLICK_STORAGE_<NAME>_*), so it stays short, lower-case, and free of
// underscores. The inline value 'sqlite_blob' can never match it.
var backendNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// ValidBackendName reports whether name can name an object backend.
func ValidBackendName(name string) bool { return backendNamePattern.MatchString(name) }

// Backend is one configured object store. MaxTotalBytes caps the cumulative
// object bytes placed on it (see db.StorageObjectBytes); 0 means unlimited and
// is allowed only when the backend is the only one.
type Backend struct {
	Name          string
	Store         ObjectStore
	MaxTotalBytes int64
}

// Registry holds the configured object backends in configuration order. A
// registry is built at process wiring from each backend's own configuration;
// no registry at all means object storage is disabled. It is read-only after
// construction and safe for concurrent use.
type Registry struct {
	backends []Backend
	byName   map[string]Backend
}

// NewRegistry validates and wraps the configured backends: at least one, each
// with a valid unique name and a store, and every one capped when there are
// several (weighted placement needs a finite free space per backend).
func NewRegistry(backends ...Backend) (*Registry, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("at least one object backend is required")
	}
	byName := make(map[string]Backend, len(backends))
	for _, b := range backends {
		if !ValidBackendName(b.Name) {
			return nil, fmt.Errorf("object backend name %q must match %s", b.Name, backendNamePattern)
		}
		if _, dup := byName[b.Name]; dup {
			return nil, fmt.Errorf("object backend %q is configured twice", b.Name)
		}
		if b.Store == nil {
			return nil, fmt.Errorf("object backend %q has no store", b.Name)
		}
		if b.MaxTotalBytes < 0 {
			return nil, fmt.Errorf("object backend %q has a negative byte ceiling", b.Name)
		}
		if len(backends) > 1 && b.MaxTotalBytes == 0 {
			return nil, fmt.Errorf("object backend %q needs a byte ceiling when several backends are configured", b.Name)
		}
		byName[b.Name] = b
	}
	return &Registry{backends: backends, byName: byName}, nil
}

// For resolves the backend a row or delete job names. An unknown name is an
// error so callers never silently read from or delete in the wrong bucket.
func (r *Registry) For(backend string) (ObjectStore, error) {
	b, ok := r.byName[backend]
	if !ok {
		return nil, fmt.Errorf("object backend %q is not configured", backend)
	}
	return b.Store, nil
}

// Each visits every configured backend in configuration order. The visit stops
// on the first error.
func (r *Registry) Each(fn func(backend string, store ObjectStore) error) error {
	for _, b := range r.backends {
		if err := fn(b.Name, b.Store); err != nil {
			return err
		}
	}
	return nil
}

// Names lists the configured backend names in configuration order.
func (r *Registry) Names() []string {
	names := make([]string, len(r.backends))
	for i, b := range r.backends {
		names[i] = b.Name
	}
	return names
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

// Choose picks the backend for a new object of size bytes. usage maps backend
// -> bytes already plausibly in the bucket (see db.StorageObjectBytes). Among
// the backends whose ceiling still fits the object, it picks at random with a
// weight equal to each one's remaining space, so fuller buckets receive fewer
// new objects and no backend is "first". It returns "" when no backend has
// room.
func (r *Registry) Choose(usage map[string]int64, size int64) string {
	return r.chooseWith(usage, size, rand.Int64N)
}

// chooseWith is Choose with the random source injected: randN(n) returns a
// value in [0, n).
func (r *Registry) chooseWith(usage map[string]int64, size int64, randN func(int64) int64) string {
	if len(r.backends) == 1 {
		b := r.backends[0]
		if b.MaxTotalBytes == 0 || usage[b.Name]+size <= b.MaxTotalBytes {
			return b.Name
		}
		return ""
	}
	type candidate struct {
		name string
		free int64
	}
	var candidates []candidate
	var total int64
	for _, b := range r.backends {
		free := b.MaxTotalBytes - usage[b.Name]
		if size > free || free <= 0 {
			continue
		}
		candidates = append(candidates, candidate{b.Name, free})
		total += free
	}
	if len(candidates) == 0 {
		return ""
	}
	pick := randN(total)
	for _, c := range candidates {
		if pick < c.free {
			return c.name
		}
		pick -= c.free
	}
	return candidates[len(candidates)-1].name
}
