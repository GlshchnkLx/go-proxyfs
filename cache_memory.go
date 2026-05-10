package proxyfs

import (
	"path"
	"strings"
	"sync"
)

type MemoryCache struct {
	mu      sync.RWMutex
	entries map[CacheKey]CacheEntry
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{entries: make(map[CacheKey]CacheEntry)}
}

func (c *MemoryCache) Get(key CacheKey) (CacheEntry, bool, error) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return CacheEntry{}, false, nil
	}
	return cloneEntry(entry), true, nil
}

func (c *MemoryCache) Put(entry CacheEntry) error {
	c.mu.Lock()
	c.entries[entry.Key] = cloneEntry(entry)
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Delete(key CacheKey) error {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Invalidate(scope Scope) error {
	root := cleanRoot(scope.TargetRoot)
	c.mu.Lock()
	for key, entry := range c.entries {
		if !routeMatches(key.RouteID, entry.Scope.RouteIDs, scope.RouteIDs) {
			continue
		}
		if !sourceMatches(entry.Scope.SourceRoots, scope.SourceRoots) {
			continue
		}
		if key.TargetPath == root || scope.Recursive && hasPathPrefix(key.TargetPath, root) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Clear() error {
	c.mu.Lock()
	c.entries = make(map[CacheKey]CacheEntry)
	c.mu.Unlock()
	return nil
}

func cloneEntry(entry CacheEntry) CacheEntry {
	entry.Children = append([]Node(nil), entry.Children...)
	entry.SourcePaths = append([]string(nil), entry.SourcePaths...)
	entry.TargetPaths = append([]string(nil), entry.TargetPaths...)
	entry.Scope.SourceRoots = append([]string(nil), entry.Scope.SourceRoots...)
	entry.Scope.RouteIDs = append([]string(nil), entry.Scope.RouteIDs...)
	return entry
}

func cleanRoot(root string) string {
	if root == "" {
		return "."
	}
	return path.Clean(root)
}

func hasPathPrefix(name, root string) bool {
	if root == "." {
		return true
	}
	return strings.HasPrefix(name, root+"/")
}

func routeMatches(keyRouteID string, entryRouteIDs, requested []string) bool {
	if len(requested) == 0 {
		return true
	}
	for _, requestedID := range requested {
		if keyRouteID == requestedID {
			return true
		}
		for _, entryRouteID := range entryRouteIDs {
			if entryRouteID == requestedID {
				return true
			}
		}
	}
	return false
}

func sourceMatches(entryRoots, requested []string) bool {
	if len(requested) == 0 {
		return true
	}
	for _, requestedRoot := range requested {
		requestedRoot = cleanRoot(requestedRoot)
		for _, entryRoot := range entryRoots {
			entryRoot = cleanRoot(entryRoot)
			if entryRoot == requestedRoot || hasPathPrefix(entryRoot, requestedRoot) || hasPathPrefix(requestedRoot, entryRoot) {
				return true
			}
		}
	}
	return false
}
