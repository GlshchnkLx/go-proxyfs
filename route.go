package proxyfs

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

type RouteConflictPolicy int

const (
	RouteConflictError RouteConflictPolicy = iota
	RouteConflictMostSpecific
	RouteConflictFirst
	RouteConflictLast
)

type Route struct {
	ID       string
	Pattern  Pattern
	Source   SourceTemplate
	Handler  Handler
	Filter   Filter
	Priority int

	order int
}

type RouteSet struct {
	configs        []routeConfig
	conflictPolicy RouteConflictPolicy
	err            error
}

type routeConfig struct {
	id        string
	pattern   string
	source    string
	hasSource bool
	handler   Handler
	filter    Filter
	priority  int
	order     int
}

func NewRouteSet() *RouteSet {
	return &RouteSet{}
}

func (routes *RouteSet) Handle(pattern string, opts ...any) *RouteSet {
	if routes == nil {
		routes = NewRouteSet()
	}
	cfg := routeConfig{
		pattern: pattern,
		order:   len(routes.configs),
	}
	for _, opt := range opts {
		switch value := opt.(type) {
		case nil:
		case Handler:
			cfg.handler = value
		case RouteOption:
			if err := value.applyRoute(&cfg); err != nil && routes.err == nil {
				routes.err = err
			}
		case Filter:
			cfg.filter = And(cfg.filter, value)
		default:
			if routes.err == nil {
				routes.err = fmt.Errorf("proxyfs: unsupported route option %T", opt)
			}
		}
	}
	routes.configs = append(routes.configs, cfg)
	return routes
}

func (routes *RouteSet) ConflictPolicy(policy RouteConflictPolicy) *RouteSet {
	if routes == nil {
		routes = NewRouteSet()
	}
	routes.conflictPolicy = policy
	return routes
}

type RouteOption interface {
	applyRoute(cfg *routeConfig) error
}

type routeOptionFunc func(cfg *routeConfig) error

func (f routeOptionFunc) applyRoute(cfg *routeConfig) error {
	return f(cfg)
}

func Source(template string) RouteOption {
	return routeOptionFunc(func(cfg *routeConfig) error {
		cfg.source = template
		cfg.hasSource = true
		return nil
	})
}

func RouteID(id string) RouteOption {
	return routeOptionFunc(func(cfg *routeConfig) error {
		if id == "" {
			return errors.New("proxyfs: route id is empty")
		}
		cfg.id = id
		return nil
	})
}

func Priority(priority int) RouteOption {
	return routeOptionFunc(func(cfg *routeConfig) error {
		cfg.priority = priority
		return nil
	})
}

func WithFilter(filter Filter) RouteOption {
	return routeOptionFunc(func(cfg *routeConfig) error {
		cfg.filter = And(cfg.filter, filter)
		return nil
	})
}

func Use(handler Handler) RouteOption {
	return routeOptionFunc(func(cfg *routeConfig) error {
		cfg.handler = handler
		return nil
	})
}

type Router struct {
	routes         []Route
	conflictPolicy RouteConflictPolicy
}

func NewRouter(routes *RouteSet) (*Router, error) {
	if routes == nil || len(routes.configs) == 0 {
		routes = NewRouteSet().Handle("/**", Identity())
	}
	if routes.err != nil {
		return nil, routes.err
	}
	compiled := make([]Route, 0, len(routes.configs))
	ids := make(map[string]struct{}, len(routes.configs))
	for i, cfg := range routes.configs {
		pattern, err := ParsePattern(cfg.pattern)
		if err != nil {
			return nil, err
		}
		if cfg.handler == nil {
			return nil, fmt.Errorf("proxyfs: route %q has no handler", pattern.String())
		}
		source := defaultSourceTemplate(pattern)
		if cfg.hasSource {
			source, err = ParseSourceTemplate(cfg.source)
			if err != nil {
				return nil, err
			}
		}
		id := cfg.id
		if id == "" {
			id = fmt.Sprintf("route-%d", i+1)
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("proxyfs: duplicate route id %q", id)
		}
		ids[id] = struct{}{}
		compiled = append(compiled, Route{
			ID:       id,
			Pattern:  pattern,
			Source:   source,
			Handler:  cfg.handler,
			Filter:   cfg.filter,
			Priority: cfg.priority,
			order:    cfg.order,
		})
	}
	if err := checkRouteAmbiguity(compiled); err != nil {
		return nil, err
	}
	return &Router{
		routes:         compiled,
		conflictPolicy: routes.conflictPolicy,
	}, nil
}

func (r *Router) Resolve(req Request) (ResolveResult, error) {
	route, match, ok, err := r.bestMatch(req.TargetPath)
	if err != nil {
		return ResolveResult{}, err
	}
	if !ok {
		return ResolveResult{}, fs.ErrNotExist
	}
	handlerReq, err := r.requestForRoute(req, route, match)
	if err != nil {
		return ResolveResult{}, err
	}
	result, err := route.Handler.Resolve(handlerReq)
	if err != nil {
		return ResolveResult{}, err
	}
	result.Cache.Scope = mergeCacheScope(result.Cache.Scope, handlerReq.TargetRoot, []string{handlerReq.SourceRoot}, []string{route.ID}, false)
	return result, nil
}

func (r *Router) ReadDir(req Request) (ReadDirResult, error) {
	merged := make(map[string]routedNode)
	var routeIDs []string
	var sourceRoots []string
	var firstErr error
	if route, match, ok, err := r.bestMatch(req.TargetPath); err != nil {
		return ReadDirResult{}, err
	} else if ok {
		handlerReq, err := r.requestForRoute(req, route, match)
		if err != nil {
			return ReadDirResult{}, err
		}
		result, err := route.Handler.ReadDir(handlerReq)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				firstErr = err
			}
		} else {
			routeIDs = append(routeIDs, route.ID)
			if len(result.Cache.Scope.SourceRoots) > 0 {
				sourceRoots = append(sourceRoots, result.Cache.Scope.SourceRoots...)
			} else {
				sourceRoots = append(sourceRoots, handlerReq.SourceRoot)
			}
			for _, node := range result.Entries {
				if err := r.mergeNode(merged, routedNode{node: node, route: route}); err != nil {
					return ReadDirResult{}, err
				}
			}
		}
	}

	for i := range r.routes {
		route := &r.routes[i]
		child, ok := route.Pattern.nextChild(req.TargetPath)
		if !ok {
			continue
		}
		sourceRoot, ok := route.Source.Instantiate(child.Params)
		if !ok {
			continue
		}
		targetRoot, ok := route.Pattern.TargetRoot(child.Params)
		if !ok {
			targetRoot = child.targetPath
		}
		info := RouteInfo{
			ID:         route.ID,
			Pattern:    route.Pattern.String(),
			TargetRoot: targetRoot,
			SourceRoot: sourceRoot,
			Priority:   route.Priority,
		}
		node := Node{
			TargetPath: child.targetPath,
			SourcePath: sourceRoot,
			Kind:       NodeDir,
			Mode:       fs.ModeDir | 0555,
			Virtual:    true,
		}
		filter := And(req.Filter, route.Filter)
		decision, err := applyFilter(filter, filterEntryForNode(FilterTarget, node, info, child.Params))
		if err != nil {
			return ReadDirResult{}, err
		}
		if decision != FilterInclude {
			continue
		}
		routeIDs = append(routeIDs, route.ID)
		sourceRoots = append(sourceRoots, sourceRoot)
		if err := r.mergeNode(merged, routedNode{node: node, route: route}); err != nil {
			return ReadDirResult{}, err
		}
	}

	if len(merged) == 0 {
		if firstErr != nil {
			return ReadDirResult{}, firstErr
		}
		return ReadDirResult{}, fs.ErrNotExist
	}
	nodes := make([]Node, 0, len(merged))
	for _, entry := range merged {
		nodes = append(nodes, entry.node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		return path.Base(nodes[i].TargetPath) < path.Base(nodes[j].TargetPath)
	})
	return ReadDirResult{
		Entries: nodes,
		Cache: CacheHint{
			Cacheable: true,
			Scope: Scope{
				TargetRoot:  req.TargetPath,
				SourceRoots: dedupeStrings(sourceRoots),
				RouteIDs:    dedupeStrings(routeIDs),
			},
		},
		Complete: true,
	}, nil
}

func (r *Router) SourcePaths(req TargetRequest) (SourcePathsResult, error) {
	route, match, ok, err := r.bestMatch(req.TargetPath)
	if err != nil {
		return SourcePathsResult{}, err
	}
	if !ok {
		return SourcePathsResult{}, fs.ErrNotExist
	}
	handlerReq, err := r.targetRequestForRoute(req, route, match)
	if err != nil {
		return SourcePathsResult{}, err
	}
	result, err := route.Handler.SourcePaths(handlerReq)
	if err != nil {
		return SourcePathsResult{}, err
	}
	result.SourcePaths = dedupeAndSort(result.SourcePaths)
	result.Cache.Scope = mergeCacheScope(result.Cache.Scope, handlerReq.TargetRoot, []string{handlerReq.SourceRoot}, []string{route.ID}, false)
	return result, nil
}

func (r *Router) TargetPaths(req SourceRequest) (TargetPathsResult, error) {
	var paths []string
	var routeIDs []string
	var sourceRoots []string
	for i := range r.routes {
		route := &r.routes[i]
		sourceMatch, ok := route.Source.MatchSource(req.SourcePath)
		if !ok {
			continue
		}
		targetRoot, ok := route.Pattern.TargetRoot(sourceMatch.Params)
		if !ok {
			continue
		}
		info := routeInfo(route, targetRoot, sourceMatch.SourceRoot)
		filter := And(req.Filter, route.Filter)
		result, err := route.Handler.TargetPaths(SourceRequest{
			Source:     req.Source,
			SourcePath: req.SourcePath,
			SourceRel:  sourceMatch.SourceRel,
			SourceRoot: sourceMatch.SourceRoot,
			TargetRoot: targetRoot,
			Route:      info,
			Params:     sourceMatch.Params.Clone(),
			Filter:     filter,
		})
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return TargetPathsResult{}, err
		}
		var matched bool
		for _, targetPath := range result.TargetPaths {
			if _, ok := route.Pattern.Match(targetPath); ok {
				paths = append(paths, targetPath)
				matched = true
			}
		}
		if matched {
			routeIDs = append(routeIDs, route.ID)
			sourceRoots = append(sourceRoots, sourceMatch.SourceRoot)
		}
	}
	if len(paths) == 0 {
		return TargetPathsResult{}, fs.ErrNotExist
	}
	return TargetPathsResult{
		TargetPaths: dedupeAndSort(paths),
		Cache: CacheHint{
			Cacheable: true,
			Scope: Scope{
				TargetRoot:  ".",
				SourceRoots: dedupeStrings(sourceRoots),
				RouteIDs:    dedupeStrings(routeIDs),
				Recursive:   true,
			},
		},
	}, nil
}

func (r *Router) RouteIDFor(targetPath string) string {
	route, _, ok, err := r.bestMatch(targetPath)
	if err != nil || !ok {
		return "_router"
	}
	return route.ID
}

func (r *Router) RouteFor(targetPath string, global Filter) (RouteInfo, Params, Filter) {
	route, match, ok, err := r.bestMatch(targetPath)
	if err != nil || !ok {
		return RouteInfo{}, nil, global
	}
	sourceRoot, _ := route.Source.Instantiate(match.Params)
	return routeInfo(route, match.TargetRoot, sourceRoot), match.Params.Clone(), And(global, route.Filter)
}

func (r *Router) bestMatch(targetPath string) (*Route, PatternMatch, bool, error) {
	var best *Route
	var bestMatch PatternMatch
	for i := range r.routes {
		route := &r.routes[i]
		match, ok := route.Pattern.Match(targetPath)
		if !ok {
			continue
		}
		if best == nil {
			best = route
			bestMatch = match
			continue
		}
		cmp := compareRoutes(route, best)
		if cmp > 0 {
			best = route
			bestMatch = match
		} else if cmp == 0 {
			return nil, PatternMatch{}, false, fmt.Errorf("proxyfs: ambiguous routes %q and %q for %q", best.Pattern.String(), route.Pattern.String(), targetPath)
		}
	}
	if best == nil {
		return nil, PatternMatch{}, false, nil
	}
	return best, bestMatch, true, nil
}

func (r *Router) requestForRoute(req Request, route *Route, match PatternMatch) (Request, error) {
	sourceRoot, ok := route.Source.Instantiate(match.Params)
	if !ok {
		return Request{}, fs.ErrNotExist
	}
	info := routeInfo(route, match.TargetRoot, sourceRoot)
	return Request{
		Source:     req.Source,
		TargetPath: req.TargetPath,
		TargetRel:  match.TargetRel,
		SourceRoot: sourceRoot,
		TargetRoot: match.TargetRoot,
		Route:      info,
		Params:     match.Params.Clone(),
		Filter:     And(req.Filter, route.Filter),
	}, nil
}

func (r *Router) targetRequestForRoute(req TargetRequest, route *Route, match PatternMatch) (TargetRequest, error) {
	sourceRoot, ok := route.Source.Instantiate(match.Params)
	if !ok {
		return TargetRequest{}, fs.ErrNotExist
	}
	info := routeInfo(route, match.TargetRoot, sourceRoot)
	return TargetRequest{
		Source:     req.Source,
		TargetPath: req.TargetPath,
		TargetRel:  match.TargetRel,
		SourceRoot: sourceRoot,
		TargetRoot: match.TargetRoot,
		Route:      info,
		Params:     match.Params.Clone(),
		Filter:     And(req.Filter, route.Filter),
	}, nil
}

func (r *Router) mergeNode(nodes map[string]routedNode, next routedNode) error {
	name := path.Base(next.node.TargetPath)
	if next.node.TargetPath == "." {
		name = "."
	}
	current, exists := nodes[name]
	if !exists {
		nodes[name] = next
		return nil
	}
	if current.node.TargetPath == next.node.TargetPath && current.node.Kind == NodeDir && next.node.Kind == NodeDir {
		if current.node.Virtual && !next.node.Virtual {
			nodes[name] = next
		}
		return nil
	}
	switch r.conflictPolicy {
	case RouteConflictFirst:
		return nil
	case RouteConflictLast:
		nodes[name] = next
		return nil
	case RouteConflictMostSpecific:
		if compareRoutes(next.route, current.route) > 0 {
			nodes[name] = next
		}
		return nil
	default:
		return ErrConflict
	}
}

type routedNode struct {
	node  Node
	route *Route
}

func routeInfo(route *Route, targetRoot, sourceRoot string) RouteInfo {
	return RouteInfo{
		ID:         route.ID,
		Pattern:    route.Pattern.String(),
		TargetRoot: targetRoot,
		SourceRoot: sourceRoot,
		Priority:   route.Priority,
	}
}

func compareRoutes(left, right *Route) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return -1
	}
	if right == nil {
		return 1
	}
	cmp := compareSpecificity(left.Pattern.specificity(), right.Pattern.specificity())
	if cmp != 0 {
		return cmp
	}
	if left.Priority != right.Priority {
		if left.Priority > right.Priority {
			return 1
		}
		return -1
	}
	return 0
}

func checkRouteAmbiguity(routes []Route) error {
	for i := range routes {
		for j := i + 1; j < len(routes); j++ {
			left := &routes[i]
			right := &routes[j]
			if left.Priority != right.Priority {
				continue
			}
			if compareSpecificity(left.Pattern.specificity(), right.Pattern.specificity()) != 0 {
				continue
			}
			if left.Pattern.canOverlap(right.Pattern) {
				return fmt.Errorf("proxyfs: ambiguous routes %q and %q", left.Pattern.String(), right.Pattern.String())
			}
		}
	}
	return nil
}

func mergeCacheScope(scope Scope, targetRoot string, sourceRoots, routeIDs []string, recursive bool) Scope {
	if scope.TargetRoot == "" {
		scope.TargetRoot = targetRoot
	}
	if len(scope.SourceRoots) == 0 {
		scope.SourceRoots = sourceRoots
	}
	if len(scope.RouteIDs) == 0 {
		scope.RouteIDs = routeIDs
	}
	scope.SourceRoots = dedupeStrings(scope.SourceRoots)
	scope.RouteIDs = dedupeStrings(scope.RouteIDs)
	scope.Recursive = scope.Recursive || recursive
	return scope
}

func dedupeAndSort(values []string) []string {
	values = dedupeStrings(values)
	sort.Strings(values)
	return values
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return append([]string(nil), out...)
}
