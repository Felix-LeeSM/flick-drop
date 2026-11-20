package storage

import (
	"fmt"
	"math/rand/v2"
	"regexp"
)

// Stored in storage_backend columns and delete events; upper-cased into FLICK_STORAGE_<NAME>_*,
// so no underscores. Can never match the inline value 'sqlite_blob'.
var backendNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

func ValidBackendName(name string) bool { return backendNamePattern.MatchString(name) }

// MaxTotalBytes 0 means unlimited, allowed only for a sole backend.
type Backend struct {
	Name          string
	Store         ObjectStore
	MaxTotalBytes int64
}

// Read-only after construction; safe for concurrent use. A nil registry means object storage is disabled.
type Registry struct {
	backends []Backend
	byName   map[string]Backend
}

// Several backends must all be capped: weighted placement needs finite free space.
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

// Unknown names are errors so callers never read or delete in the wrong bucket.
func (r *Registry) For(backend string) (ObjectStore, error) {
	b, ok := r.byName[backend]
	if !ok {
		return nil, fmt.Errorf("object backend %q is not configured", backend)
	}
	return b.Store, nil
}

func (r *Registry) Each(fn func(backend string, store ObjectStore) error) error {
	for _, b := range r.backends {
		if err := fn(b.Name, b.Store); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) Names() []string {
	names := make([]string, len(r.backends))
	for i, b := range r.backends {
		names[i] = b.Name
	}
	return names
}

// The check keeps a future read-only backend from failing finalize deep inside a transaction.
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

// Weighted by remaining space so fuller buckets get fewer objects. Returns "" when none has room.
func (r *Registry) Choose(usage map[string]int64, size int64) string {
	return r.chooseWith(usage, size, rand.Int64N)
}

// randN(n) returns a value in [0, n).
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
