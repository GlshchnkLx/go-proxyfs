package proxyfs

import (
	"io/fs"
	"time"
)

type TransformConfig struct {
	MaxDepth       int
	ConflictPolicy ConflictPolicy
	MapFile        func(MapContext, SourceEntry) ([]Node, error)
	MapDir         func(MapContext, SourceEntry) ([]Node, error)
	TTL            time.Duration
}

type MapContext struct {
	Request Request
}

type transformHandler struct {
	cfg TransformConfig
}

func Transform(cfg TransformConfig) Handler {
	return transformHandler{cfg: cfg}
}

func (h transformHandler) Resolve(req Request) (ResolveResult, error) {
	if req.TargetRel == "." {
		node, err := virtualRootNode(req, true)
		if err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{Node: node, Cache: cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true)}, nil
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

func (h transformHandler) ReadDir(req Request) (ReadDirResult, error) {
	nodes, err := h.materialize(req)
	if err != nil {
		return ReadDirResult{}, err
	}
	var children []Node
	for _, node := range nodes {
		if parentPath(node.TargetPath) == req.TargetPath {
			children = append(children, node)
		}
	}
	if len(children) == 0 {
		return ReadDirResult{}, fs.ErrNotExist
	}
	sortNodes(children)
	return ReadDirResult{
		Entries:  children,
		Cache:    cacheHint(h.cfg.TTL, req.TargetRoot, []string{req.SourceRoot}, []string{req.Route.ID}, true),
		Complete: true,
	}, nil
}

func (h transformHandler) SourcePaths(req TargetRequest) (SourcePathsResult, error) {
	result, err := h.Resolve(Request(req))
	if err != nil {
		return SourcePathsResult{}, err
	}
	return SourcePathsResult{SourcePaths: sourcePathsForNode(result.Node), Cache: result.Cache}, nil
}

func (h transformHandler) TargetPaths(req SourceRequest) (TargetPathsResult, error) {
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

func (h transformHandler) materialize(req Request) ([]Node, error) {
	return Materializer{
		MaxDepth:       h.cfg.MaxDepth,
		ConflictPolicy: h.cfg.ConflictPolicy,
		Mapper: transformMapper{
			ctx:     MapContext{Request: req},
			mapDir:  h.cfg.MapDir,
			mapFile: h.cfg.MapFile,
		},
	}.Materialize(req)
}

type transformMapper struct {
	ctx     MapContext
	mapFile func(MapContext, SourceEntry) ([]Node, error)
	mapDir  func(MapContext, SourceEntry) ([]Node, error)
}

func (m transformMapper) Map(entry SourceEntry) ([]Node, error) {
	if entry.Info.IsDir() {
		if m.mapDir == nil {
			return nil, nil
		}
		return m.mapDir(m.ctx, entry)
	}
	if m.mapFile == nil {
		return nil, nil
	}
	return m.mapFile(m.ctx, entry)
}
