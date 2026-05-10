package proxyfs

import (
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type identityHandler struct {
	ttl time.Duration
}

func Identity() Handler {
	return identityHandler{}
}

func (h identityHandler) Resolve(req Request) (ResolveResult, error) {
	sourcePath := joinPath(req.SourceRoot, req.TargetRel)
	info, err := fs.Stat(req.Source, sourcePath)
	if err != nil {
		return ResolveResult{}, unwrapPathErr(err)
	}
	node := NodeFromFileInfo(req.TargetPath, sourcePath, info)
	decision, err := applyFilter(req.Filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
	if err != nil {
		return ResolveResult{}, err
	}
	if decision != FilterInclude {
		return ResolveResult{}, fs.ErrNotExist
	}
	return ResolveResult{
		Node:  node,
		Cache: cacheHint(h.ttl, req.TargetPath, []string{sourcePath}, []string{req.Route.ID}, false),
	}, nil
}

func (h identityHandler) ReadDir(req Request) (ReadDirResult, error) {
	sourcePath := joinPath(req.SourceRoot, req.TargetRel)
	entries, err := fs.ReadDir(req.Source, sourcePath)
	if err != nil {
		return ReadDirResult{}, unwrapPathErr(err)
	}
	nodes := make([]Node, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return ReadDirResult{}, unwrapPathErr(err)
		}
		childSourcePath := joinPath(sourcePath, entry.Name())
		childTargetPath := joinPath(req.TargetPath, entry.Name())
		node := NodeFromFileInfo(childTargetPath, childSourcePath, info)
		decision, err := applyFilter(req.Filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
		if err != nil {
			return ReadDirResult{}, err
		}
		if decision != FilterInclude {
			continue
		}
		nodes = append(nodes, node)
	}
	sortNodes(nodes)
	return ReadDirResult{
		Entries:  nodes,
		Cache:    cacheHint(h.ttl, req.TargetPath, []string{sourcePath}, []string{req.Route.ID}, false),
		Complete: true,
	}, nil
}

func (h identityHandler) SourcePaths(req TargetRequest) (SourcePathsResult, error) {
	result, err := h.Resolve(Request{
		Source:     req.Source,
		TargetPath: req.TargetPath,
		TargetRel:  req.TargetRel,
		SourceRoot: req.SourceRoot,
		TargetRoot: req.TargetRoot,
		Route:      req.Route,
		Params:     req.Params,
		Filter:     req.Filter,
	})
	if err != nil {
		return SourcePathsResult{}, err
	}
	return SourcePathsResult{
		SourcePaths: sourcePathsForNode(result.Node),
		Cache:       result.Cache,
	}, nil
}

func (h identityHandler) TargetPaths(req SourceRequest) (TargetPathsResult, error) {
	info, err := fs.Stat(req.Source, req.SourcePath)
	if err != nil {
		return TargetPathsResult{}, unwrapPathErr(err)
	}
	targetPath := joinPath(req.TargetRoot, req.SourceRel)
	node := NodeFromFileInfo(targetPath, req.SourcePath, info)
	decision, err := applyFilter(req.Filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
	if err != nil {
		return TargetPathsResult{}, err
	}
	if decision != FilterInclude {
		return TargetPathsResult{}, fs.ErrNotExist
	}
	return TargetPathsResult{
		TargetPaths: []string{targetPath},
		Cache:       cacheHint(h.ttl, targetPath, []string{req.SourcePath}, []string{req.Route.ID}, false),
	}, nil
}

type mountHandler struct {
	sourceRoot string
	identity   identityHandler
}

func Mount(sourceRoot string) Handler {
	return mountHandler{sourceRoot: cleanRoot(strings.TrimPrefix(sourceRoot, "/"))}
}

func (h mountHandler) request(req Request) Request {
	if h.sourceRoot != "" {
		req.SourceRoot = h.sourceRoot
		req.Route.SourceRoot = h.sourceRoot
	}
	return req
}

func (h mountHandler) sourceRequest(req SourceRequest) SourceRequest {
	if h.sourceRoot != "" {
		req.SourceRoot = h.sourceRoot
		req.Route.SourceRoot = h.sourceRoot
	}
	return req
}

func (h mountHandler) targetRequest(req TargetRequest) TargetRequest {
	if h.sourceRoot != "" {
		req.SourceRoot = h.sourceRoot
		req.Route.SourceRoot = h.sourceRoot
	}
	return req
}

func (h mountHandler) Resolve(req Request) (ResolveResult, error) {
	return h.identity.Resolve(h.request(req))
}

func (h mountHandler) ReadDir(req Request) (ReadDirResult, error) {
	return h.identity.ReadDir(h.request(req))
}

func (h mountHandler) SourcePaths(req TargetRequest) (SourcePathsResult, error) {
	return h.identity.SourcePaths(h.targetRequest(req))
}

func (h mountHandler) TargetPaths(req SourceRequest) (TargetPathsResult, error) {
	return h.identity.TargetPaths(h.sourceRequest(req))
}

type FlatConfig struct {
	MaxDepth       int
	ConflictPolicy ConflictPolicy
	TTL            time.Duration
}

type flatHandler struct {
	cfg FlatConfig
}

func Flat(cfg FlatConfig) Handler {
	return flatHandler{cfg: cfg}
}

func (h flatHandler) Resolve(req Request) (ResolveResult, error) {
	if req.TargetRel == "." {
		node, err := virtualRootNode(req, true)
		if err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{Node: node, Cache: cacheHint(h.cfg.TTL, req.TargetPath, []string{req.SourceRoot}, []string{req.Route.ID}, true)}, nil
	}
	if strings.Contains(req.TargetRel, "/") {
		return ResolveResult{}, fs.ErrNotExist
	}
	nodes, err := h.materialize(req)
	if err != nil {
		return ResolveResult{}, err
	}
	for _, node := range nodes {
		if node.TargetPath == req.TargetPath {
			return ResolveResult{Node: node, Cache: cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true)}, nil
		}
	}
	return ResolveResult{}, fs.ErrNotExist
}

func (h flatHandler) ReadDir(req Request) (ReadDirResult, error) {
	if req.TargetRel != "." {
		return ReadDirResult{}, fs.ErrNotExist
	}
	nodes, err := h.materialize(req)
	if err != nil {
		return ReadDirResult{}, err
	}
	return ReadDirResult{
		Entries:  nodes,
		Cache:    cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true),
		Complete: true,
	}, nil
}

func (h flatHandler) SourcePaths(req TargetRequest) (SourcePathsResult, error) {
	result, err := h.Resolve(Request(req))
	if err != nil {
		return SourcePathsResult{}, err
	}
	return SourcePathsResult{SourcePaths: sourcePathsForNode(result.Node), Cache: result.Cache}, nil
}

func (h flatHandler) TargetPaths(req SourceRequest) (TargetPathsResult, error) {
	nodes, err := h.materialize(Request{
		Source:     req.Source,
		TargetPath: req.TargetRoot,
		TargetRel:  ".",
		SourceRoot: req.SourceRoot,
		TargetRoot: req.TargetRoot,
		Route:      req.Route,
		Params:     req.Params,
		Filter:     req.Filter,
	})
	if err != nil {
		return TargetPathsResult{}, err
	}
	var paths []string
	for _, node := range nodes {
		if node.SourcePath == req.SourcePath {
			paths = append(paths, node.TargetPath)
		}
	}
	if len(paths) == 0 {
		return TargetPathsResult{}, fs.ErrNotExist
	}
	return TargetPathsResult{
		TargetPaths: dedupeAndSort(paths),
		Cache:       cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true),
	}, nil
}

func (h flatHandler) materialize(req Request) ([]Node, error) {
	materializer := Materializer{
		MaxDepth:       h.cfg.MaxDepth,
		ConflictPolicy: h.cfg.ConflictPolicy,
		Mapper:         flatMapper{},
	}
	return materializer.Materialize(req)
}

type flatMapper struct{}

func (flatMapper) Map(entry SourceEntry) ([]Node, error) {
	if entry.Info.IsDir() {
		return nil, nil
	}
	targetPath := joinPath(entry.TargetRoot, path.Base(entry.SourcePath))
	return []Node{NodeFromFileInfo(targetPath, entry.SourcePath, entry.Info)}, nil
}

type ClassifierConfig struct {
	MaxDepth       int
	Categories     map[string]string
	Fallback       string
	ConflictPolicy ConflictPolicy
	TTL            time.Duration
}

type classifierHandler struct {
	cfg        ClassifierConfig
	categories map[string]string
	fallback   string
}

func Classifier(cfg ClassifierConfig) Handler {
	categories := normalizeCategories(cfg.Categories)
	if len(categories) == 0 {
		categories = normalizeCategories(DefaultCategories())
	}
	fallback := strings.TrimSpace(cfg.Fallback)
	if fallback == "" {
		fallback = "Other"
	}
	return classifierHandler{
		cfg:        cfg,
		categories: categories,
		fallback:   fallback,
	}
}

func DefaultCategories() map[string]string {
	return map[string]string{
		".txt":  "Text",
		".md":   "Text",
		".csv":  "Text",
		".png":  "Image",
		".jpg":  "Image",
		".jpeg": "Image",
		".gif":  "Image",
		".webp": "Image",
		".pdf":  "Document",
		".doc":  "Document",
		".docx": "Document",
		".xls":  "Document",
		".xlsx": "Document",
	}
}

func (h classifierHandler) Resolve(req Request) (ResolveResult, error) {
	if req.TargetRel == "." {
		node, err := virtualRootNode(req, true)
		if err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{Node: node, Cache: cacheHint(h.cfg.TTL, req.TargetPath, []string{req.SourceRoot}, []string{req.Route.ID}, true)}, nil
	}
	parts := strings.Split(req.TargetRel, "/")
	if len(parts) == 1 {
		nodes, err := h.materialize(req)
		if err != nil {
			return ResolveResult{}, err
		}
		if hasCategory(nodes, req.TargetPath) {
			return ResolveResult{
				Node:  categoryNode(req.TargetRoot, parts[0]),
				Cache: cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true),
			}, nil
		}
		return ResolveResult{}, fs.ErrNotExist
	}
	if len(parts) != 2 {
		return ResolveResult{}, fs.ErrNotExist
	}
	nodes, err := h.materialize(req)
	if err != nil {
		return ResolveResult{}, err
	}
	for _, node := range nodes {
		if node.TargetPath == req.TargetPath {
			return ResolveResult{Node: node, Cache: cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true)}, nil
		}
	}
	return ResolveResult{}, fs.ErrNotExist
}

func (h classifierHandler) ReadDir(req Request) (ReadDirResult, error) {
	nodes, err := h.materialize(req)
	if err != nil {
		return ReadDirResult{}, err
	}
	switch req.TargetRel {
	case ".":
		return ReadDirResult{
			Entries:  categoryNodes(req.TargetRoot, nodes),
			Cache:    cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true),
			Complete: true,
		}, nil
	default:
		if strings.Contains(req.TargetRel, "/") {
			return ReadDirResult{}, fs.ErrNotExist
		}
		prefix := joinPath(req.TargetRoot, req.TargetRel)
		filtered := nodes[:0]
		for _, node := range nodes {
			if path.Dir(node.TargetPath) == prefix {
				filtered = append(filtered, node)
			}
		}
		if len(filtered) == 0 {
			return ReadDirResult{}, fs.ErrNotExist
		}
		sortNodes(filtered)
		return ReadDirResult{
			Entries:  append([]Node(nil), filtered...),
			Cache:    cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true),
			Complete: true,
		}, nil
	}
}

func (h classifierHandler) SourcePaths(req TargetRequest) (SourcePathsResult, error) {
	result, err := h.Resolve(Request(req))
	if err != nil {
		return SourcePathsResult{}, err
	}
	return SourcePathsResult{SourcePaths: sourcePathsForNode(result.Node), Cache: result.Cache}, nil
}

func (h classifierHandler) TargetPaths(req SourceRequest) (TargetPathsResult, error) {
	nodes, err := h.materialize(Request{
		Source:     req.Source,
		TargetPath: req.TargetRoot,
		TargetRel:  ".",
		SourceRoot: req.SourceRoot,
		TargetRoot: req.TargetRoot,
		Route:      req.Route,
		Params:     req.Params,
		Filter:     req.Filter,
	})
	if err != nil {
		return TargetPathsResult{}, err
	}
	var paths []string
	for _, node := range nodes {
		if node.SourcePath == req.SourcePath {
			paths = append(paths, node.TargetPath)
		}
	}
	if len(paths) == 0 {
		return TargetPathsResult{}, fs.ErrNotExist
	}
	return TargetPathsResult{
		TargetPaths: dedupeAndSort(paths),
		Cache:       cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true),
	}, nil
}

func (h classifierHandler) materialize(req Request) ([]Node, error) {
	materializer := Materializer{
		MaxDepth:       h.cfg.MaxDepth,
		ConflictPolicy: h.cfg.ConflictPolicy,
		Mapper: classifierMapper{
			categories: h.categories,
			fallback:   h.fallback,
		},
	}
	return materializer.Materialize(req)
}

type classifierMapper struct {
	categories map[string]string
	fallback   string
}

func (m classifierMapper) Map(entry SourceEntry) ([]Node, error) {
	if entry.Info.IsDir() {
		return nil, nil
	}
	category := m.categories[strings.ToLower(path.Ext(entry.SourcePath))]
	if category == "" {
		category = m.fallback
	}
	category = strings.Trim(strings.TrimSpace(category), "/")
	if category == "" || strings.Contains(category, "/") {
		category = "Other"
	}
	targetPath := joinPath(entry.TargetRoot, category, path.Base(entry.SourcePath))
	return []Node{NodeFromFileInfo(targetPath, entry.SourcePath, entry.Info)}, nil
}

func NodeFromFileInfo(targetPath, sourcePath string, info fs.FileInfo) Node {
	kind := NodeFile
	if info.IsDir() {
		kind = NodeDir
	}
	return Node{
		TargetPath: targetPath,
		SourcePath: sourcePath,
		Kind:       kind,
		Mode:       info.Mode(),
		Size:       info.Size(),
		ModTime:    info.ModTime(),
	}
}

func virtualRootNode(req Request, forceVirtual bool) (Node, error) {
	info, err := fs.Stat(req.Source, req.SourceRoot)
	if err != nil {
		return Node{}, unwrapPathErr(err)
	}
	if !info.IsDir() {
		return Node{}, fs.ErrInvalid
	}
	node := NodeFromFileInfo(req.TargetRoot, req.SourceRoot, info)
	node.Virtual = forceVirtual
	decision, err := applyFilter(req.Filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
	if err != nil {
		return Node{}, err
	}
	if decision != FilterInclude {
		return Node{}, fs.ErrNotExist
	}
	return node, nil
}

func normalizeCategories(categories map[string]string) map[string]string {
	normalized := make(map[string]string, len(categories))
	for ext, category := range categories {
		ext = strings.TrimSpace(strings.ToLower(ext))
		category = strings.TrimSpace(category)
		if ext == "" || category == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		normalized[ext] = category
	}
	return normalized
}

func categoryNodes(root string, nodes []Node) []Node {
	seen := make(map[string]bool)
	var categories []Node
	for _, node := range nodes {
		dir := path.Dir(node.TargetPath)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		categories = append(categories, categoryNode(root, path.Base(dir)))
	}
	sortNodes(categories)
	return categories
}

func hasCategory(nodes []Node, categoryPath string) bool {
	for _, node := range nodes {
		if path.Dir(node.TargetPath) == categoryPath {
			return true
		}
	}
	return false
}

func categoryNode(root, category string) Node {
	return Node{
		TargetPath: joinPath(root, category),
		SourcePath: root,
		Kind:       NodeDir,
		Mode:       fs.ModeDir | 0555,
		Virtual:    true,
	}
}

func sortNodes(nodes []Node) {
	sort.Slice(nodes, func(a, b int) bool {
		if path.Base(nodes[a].TargetPath) == path.Base(nodes[b].TargetPath) {
			return nodes[a].TargetPath < nodes[b].TargetPath
		}
		return path.Base(nodes[a].TargetPath) < path.Base(nodes[b].TargetPath)
	})
}

func cacheHint(ttl time.Duration, targetRoot string, sourceRoots, routeIDs []string, recursive bool) CacheHint {
	return CacheHint{
		TTL:       ttl,
		Cacheable: true,
		Scope: Scope{
			TargetRoot:  targetRoot,
			SourceRoots: dedupeStrings(sourceRoots),
			RouteIDs:    dedupeStrings(routeIDs),
			Recursive:   recursive,
		},
	}
}

func uniqueTargetPath(existing map[string]Node, counts map[string]int, targetPath string) string {
	if _, exists := existing[targetPath]; !exists {
		counts[targetPath] = 1
		return targetPath
	}
	dir := path.Dir(targetPath)
	ext := path.Ext(targetPath)
	base := strings.TrimSuffix(path.Base(targetPath), ext)
	for n := counts[targetPath] + 1; ; n++ {
		candidate := joinPath(dir, base+"_"+strconv.Itoa(n)+ext)
		if _, exists := existing[candidate]; !exists {
			counts[targetPath] = n
			return candidate
		}
	}
}
