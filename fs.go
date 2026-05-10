package proxyfs

import (
	"errors"
	"io/fs"
	"sort"
	"time"
)

type FS struct {
	source      fs.FS
	router      *Router
	cache       Cache
	filter      Filter
	defaultTTL  time.Duration
	negativeTTL time.Duration
}

type Config struct {
	Source fs.FS
	Routes *RouteSet
	Cache  Cache
	Filter Filter

	DefaultTTL  time.Duration
	NegativeTTL time.Duration
}

var _ fs.FS = (*FS)(nil)
var _ fs.ReadDirFS = (*FS)(nil)
var _ fs.StatFS = (*FS)(nil)

func New(cfg Config) (*FS, error) {
	if cfg.Source == nil {
		return nil, errors.New("proxyfs: source is required")
	}
	if cfg.Cache == nil {
		cfg.Cache = NewMemoryCache()
	}
	if cfg.Filter == nil {
		cfg.Filter = AllowAll()
	}
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = time.Minute
	}
	if cfg.NegativeTTL == 0 {
		cfg.NegativeTTL = 10 * time.Second
	}
	router, err := NewRouter(cfg.Routes)
	if err != nil {
		return nil, err
	}
	return &FS{
		source:      cfg.Source,
		router:      router,
		cache:       cfg.Cache,
		filter:      cfg.Filter,
		defaultTTL:  cfg.DefaultTTL,
		negativeTTL: cfg.NegativeTTL,
	}, nil
}

func (p *FS) Open(name string) (fs.File, error) {
	targetPath, err := cleanTargetPath(name)
	if err != nil {
		return nil, pathError("open", name, err)
	}
	node, err := p.resolve(targetPath)
	if err != nil {
		return nil, pathError("open", targetPath, err)
	}
	if err := p.applyOpenFilter(node); err != nil {
		return nil, pathError("open", targetPath, err)
	}
	if node.Kind == NodeDir {
		children, err := p.readDirNodes(targetPath)
		if err != nil {
			return nil, pathError("open", targetPath, err)
		}
		return newVirtualDirFile(node, children), nil
	}
	if node.Opener != nil {
		file, err := node.Opener.Open(OpenRequest{
			Source: p.source,
			Node:   node,
			Route:  p.routeInfo(node.TargetPath),
			Params: p.routeParams(node.TargetPath),
		})
		if err != nil {
			return nil, pathError("open", targetPath, unwrapPathErr(err))
		}
		return file, nil
	}
	file, err := p.source.Open(node.SourcePath)
	if err != nil {
		return nil, pathError("open", targetPath, unwrapPathErr(err))
	}
	return file, nil
}

func (p *FS) Stat(name string) (fs.FileInfo, error) {
	targetPath, err := cleanTargetPath(name)
	if err != nil {
		return nil, pathError("stat", name, err)
	}
	node, err := p.resolve(targetPath)
	if err != nil {
		return nil, pathError("stat", targetPath, err)
	}
	return nodeInfo{node: node}, nil
}

func (p *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	targetPath, err := cleanTargetPath(name)
	if err != nil {
		return nil, pathError("readdir", name, err)
	}
	children, err := p.readDirNodes(targetPath)
	if err != nil {
		return nil, pathError("readdir", targetPath, err)
	}
	entries := make([]fs.DirEntry, 0, len(children))
	for _, child := range children {
		entries = append(entries, nodeDirEntry{node: child})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return entries, nil
}

func (p *FS) Invalidate(scope Scope) error {
	return p.cache.Invalidate(scope)
}

func (p *FS) InvalidatePath(name string) error {
	targetPath, err := cleanTargetPath(name)
	if err != nil {
		return pathError("invalidate", name, err)
	}
	if err := p.cache.Invalidate(Scope{TargetRoot: targetPath}); err != nil {
		return err
	}
	return p.cache.Invalidate(Scope{TargetRoot: parentPath(targetPath)})
}

func (p *FS) ClearCache() error {
	return p.cache.Clear()
}

func (p *FS) SourcePaths(targetPath string) ([]string, error) {
	targetPath, err := cleanTargetPath(targetPath)
	if err != nil {
		return nil, pathError("sourcepaths", targetPath, err)
	}
	key := CacheKey{TargetPath: targetPath, RouteID: p.router.RouteIDFor(targetPath), Kind: CacheSourcePaths}
	now := time.Now()
	if entry, ok, err := p.cache.Get(key); err != nil {
		return nil, pathError("sourcepaths", targetPath, err)
	} else if ok && !entry.expired(now) {
		if entry.Negative {
			return nil, pathError("sourcepaths", targetPath, entry.Err)
		}
		return append([]string(nil), entry.SourcePaths...), nil
	}
	result, err := p.router.SourcePaths(TargetRequest{
		Source:     p.source,
		TargetPath: targetPath,
		Filter:     p.filter,
	})
	if err != nil {
		p.cacheNegative(key, err)
		return nil, pathError("sourcepaths", targetPath, err)
	}
	p.cachePaths(key, result.SourcePaths, nil, result.Cache)
	return append([]string(nil), result.SourcePaths...), nil
}

func (p *FS) TargetPaths(sourcePath string) ([]string, error) {
	sourcePath, err := cleanTargetPath(sourcePath)
	if err != nil {
		return nil, pathError("targetpaths", sourcePath, err)
	}
	key := CacheKey{TargetPath: sourcePath, RouteID: "_router", Kind: CacheTargetPaths}
	now := time.Now()
	if entry, ok, err := p.cache.Get(key); err != nil {
		return nil, pathError("targetpaths", sourcePath, err)
	} else if ok && !entry.expired(now) {
		if entry.Negative {
			return nil, pathError("targetpaths", sourcePath, entry.Err)
		}
		return append([]string(nil), entry.TargetPaths...), nil
	}
	result, err := p.router.TargetPaths(SourceRequest{
		Source:     p.source,
		SourcePath: sourcePath,
		Filter:     p.filter,
	})
	if err != nil {
		p.cacheNegative(key, err)
		return nil, pathError("targetpaths", sourcePath, err)
	}
	p.cachePaths(key, nil, result.TargetPaths, result.Cache)
	return append([]string(nil), result.TargetPaths...), nil
}

func (p *FS) resolve(targetPath string) (Node, error) {
	now := time.Now()
	key := CacheKey{TargetPath: targetPath, RouteID: p.router.RouteIDFor(targetPath), Kind: CacheNode}
	if entry, ok, err := p.cache.Get(key); err != nil {
		return Node{}, err
	} else if ok && !entry.expired(now) {
		if entry.Negative {
			return Node{}, entry.Err
		}
		if entry.Node.Kind == 0 {
			return Node{}, fs.ErrNotExist
		}
		return entry.Node, entry.Err
	}
	result, err := p.router.Resolve(Request{
		Source:     p.source,
		TargetPath: targetPath,
		Filter:     p.filter,
	})
	if err != nil {
		p.cacheNegative(key, err)
		return Node{}, err
	}
	if ttl, ok := p.cacheTTL(result.Cache); ok {
		_ = p.cache.Put(CacheEntry{
			Key:         key,
			Node:        result.Node,
			SourcePaths: sourcePathsForNode(result.Node),
			ExpiresAt:   expiresAt(ttl),
			Scope:       result.Cache.Scope,
		})
	}
	return result.Node, nil
}

func (p *FS) readDirNodes(targetPath string) ([]Node, error) {
	now := time.Now()
	key := CacheKey{TargetPath: targetPath, RouteID: "_router", Kind: CacheDir}
	if entry, ok, err := p.cache.Get(key); err != nil {
		return nil, err
	} else if ok && !entry.expired(now) {
		if entry.Negative {
			return nil, entry.Err
		}
		return append([]Node(nil), entry.Children...), nil
	}
	result, err := p.router.ReadDir(Request{
		Source:     p.source,
		TargetPath: targetPath,
		Filter:     p.filter,
	})
	if err != nil {
		p.cacheNegative(key, err)
		return nil, err
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		return result.Entries[i].TargetPath < result.Entries[j].TargetPath
	})
	if ttl, ok := p.cacheTTL(result.Cache); ok {
		_ = p.cache.Put(CacheEntry{
			Key:       key,
			Children:  result.Entries,
			ExpiresAt: expiresAt(ttl),
			Scope:     result.Cache.Scope,
		})
		for _, child := range result.Entries {
			childRouteID := p.router.RouteIDFor(child.TargetPath)
			if len(result.Cache.Scope.RouteIDs) > 0 && !stringIn(childRouteID, result.Cache.Scope.RouteIDs) {
				continue
			}
			childKey := CacheKey{TargetPath: child.TargetPath, RouteID: childRouteID, Kind: CacheNode}
			_ = p.cache.Put(CacheEntry{
				Key:         childKey,
				Node:        child,
				SourcePaths: sourcePathsForNode(child),
				ExpiresAt:   expiresAt(ttl),
				Scope:       result.Cache.Scope,
			})
		}
	}
	return append([]Node(nil), result.Entries...), nil
}

func (p *FS) cacheNegative(key CacheKey, err error) {
	if p.negativeTTL < 0 || !errors.Is(err, fs.ErrNotExist) {
		return
	}
	_ = p.cache.Put(CacheEntry{
		Key:       key,
		Err:       fs.ErrNotExist,
		ExpiresAt: expiresAt(p.negativeTTL),
		Negative:  true,
		Scope: Scope{
			TargetRoot: key.TargetPath,
			RouteIDs:   []string{key.RouteID},
		},
	})
}

func (p *FS) cachePaths(key CacheKey, sourcePaths, targetPaths []string, hint CacheHint) {
	if ttl, ok := p.cacheTTL(hint); ok {
		_ = p.cache.Put(CacheEntry{
			Key:         key,
			SourcePaths: append([]string(nil), sourcePaths...),
			TargetPaths: append([]string(nil), targetPaths...),
			ExpiresAt:   expiresAt(ttl),
			Scope:       hint.Scope,
		})
	}
}

func (p *FS) cacheTTL(hint CacheHint) (time.Duration, bool) {
	if !hint.Cacheable || hint.TTL < 0 {
		return 0, false
	}
	return ttlOrDefault(hint.TTL, p.defaultTTL), true
}

func (p *FS) applyOpenFilter(node Node) error {
	route, params, filter := p.router.RouteFor(node.TargetPath, p.filter)
	decision, err := applyFilter(filter, filterEntryForNode(FilterOpen, node, route, params))
	if err != nil {
		return err
	}
	switch decision {
	case FilterInclude:
		return nil
	case FilterDeny:
		return fs.ErrPermission
	default:
		return fs.ErrNotExist
	}
}

func (p *FS) routeInfo(targetPath string) RouteInfo {
	route, _, _ := p.router.RouteFor(targetPath, p.filter)
	return route
}

func (p *FS) routeParams(targetPath string) Params {
	_, params, _ := p.router.RouteFor(targetPath, p.filter)
	return params
}

func sourcePathsForNode(node Node) []string {
	if node.SourcePath == "" {
		return nil
	}
	return []string{node.SourcePath}
}

func stringIn(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func ttlOrDefault(ttl, def time.Duration) time.Duration {
	if ttl == 0 {
		return def
	}
	return ttl
}

func expiresAt(ttl time.Duration) time.Time {
	if ttl < 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

func unwrapPathErr(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
