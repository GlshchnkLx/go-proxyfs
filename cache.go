package proxyfs

import "time"

type Cache interface {
	Get(key CacheKey) (CacheEntry, bool, error)
	Put(entry CacheEntry) error
	Delete(key CacheKey) error
	Invalidate(scope Scope) error
	Clear() error
}

type CacheKey struct {
	TargetPath string
	RouteID    string
	Kind       CacheKind
}

type CacheKind uint8

const (
	CacheNode CacheKind = iota + 1
	CacheDir
	CacheSourcePaths
	CacheTargetPaths
)

type CacheEntry struct {
	Key CacheKey

	Node     Node
	Children []Node

	SourcePaths []string
	TargetPaths []string

	Err      error
	Negative bool

	ExpiresAt time.Time
	Scope     Scope
}

func (entry CacheEntry) expired(now time.Time) bool {
	return !entry.ExpiresAt.IsZero() && !now.Before(entry.ExpiresAt)
}
