package proxyfs

import (
	"encoding/json"
	"io/fs"
	"path"
	"strings"
	"time"
)

type VirtualProvider interface {
	Resolve(req Request) (ResolveResult, error)
	ReadDir(req Request) (ReadDirResult, error)
}

type VirtualContext struct {
	Source     fs.FS
	TargetPath string
	TargetRel  string
	TargetRoot string
	SourceRoot string
	Route      RouteInfo
	Params     Params
}

type virtualHandler struct {
	provider VirtualProvider
}

func Virtual(provider VirtualProvider) Handler {
	return virtualHandler{provider: provider}
}

func (h virtualHandler) Resolve(req Request) (ResolveResult, error) {
	if h.provider == nil {
		return ResolveResult{}, fs.ErrNotExist
	}
	result, err := h.provider.Resolve(req)
	if err != nil {
		return ResolveResult{}, err
	}
	result.Cache.Scope = mergeCacheScope(result.Cache.Scope, req.TargetRoot, nil, []string{req.Route.ID}, false)
	return result, nil
}

func (h virtualHandler) ReadDir(req Request) (ReadDirResult, error) {
	if h.provider == nil {
		return ReadDirResult{}, fs.ErrNotExist
	}
	result, err := h.provider.ReadDir(req)
	if err != nil {
		return ReadDirResult{}, err
	}
	result.Cache.Scope = mergeCacheScope(result.Cache.Scope, req.TargetRoot, nil, []string{req.Route.ID}, false)
	return result, nil
}

func (h virtualHandler) SourcePaths(req TargetRequest) (SourcePathsResult, error) {
	result, err := h.Resolve(Request(req))
	if err != nil {
		return SourcePathsResult{}, err
	}
	return SourcePathsResult{
		SourcePaths: sourcePathsForNode(result.Node),
		Cache:       result.Cache,
	}, nil
}

func (h virtualHandler) TargetPaths(SourceRequest) (TargetPathsResult, error) {
	return TargetPathsResult{}, fs.ErrNotExist
}

type namedProvider interface {
	VirtualProvider
	providerName() string
}

type staticDirProvider struct {
	name     string
	children []namedProvider
}

func StaticDir(children ...VirtualProvider) VirtualProvider {
	named := make([]namedProvider, 0, len(children))
	for _, child := range children {
		if child == nil {
			continue
		}
		if provider, ok := child.(namedProvider); ok {
			named = append(named, provider)
		}
	}
	return staticDirProvider{children: named}
}

func Dir(name string, children ...VirtualProvider) VirtualProvider {
	named := make([]namedProvider, 0, len(children))
	for _, child := range children {
		if child == nil {
			continue
		}
		if provider, ok := child.(namedProvider); ok {
			named = append(named, provider)
		}
	}
	return staticDirProvider{name: name, children: named}
}

func (p staticDirProvider) providerName() string {
	return p.name
}

func (p staticDirProvider) Resolve(req Request) (ResolveResult, error) {
	localRel, ok := p.localRel(req.TargetRel)
	if !ok {
		return ResolveResult{}, fs.ErrNotExist
	}
	if localRel != "." {
		child, childReq, ok := p.childRequest(req, localRel)
		if !ok {
			return ResolveResult{}, fs.ErrNotExist
		}
		return child.Resolve(childReq)
	}
	node := Node{
		TargetPath: req.TargetPath,
		Kind:       NodeDir,
		Mode:       fs.ModeDir | 0555,
		Virtual:    true,
	}
	decision, err := applyFilter(req.Filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
	if err != nil {
		return ResolveResult{}, err
	}
	if decision != FilterInclude {
		return ResolveResult{}, fs.ErrNotExist
	}
	return ResolveResult{Node: node, Cache: cacheHint(0, req.TargetPath, nil, []string{req.Route.ID}, false)}, nil
}

func (p staticDirProvider) ReadDir(req Request) (ReadDirResult, error) {
	localRel, ok := p.localRel(req.TargetRel)
	if !ok {
		return ReadDirResult{}, fs.ErrNotExist
	}
	if localRel != "." {
		child, childReq, ok := p.childRequest(req, localRel)
		if !ok {
			return ReadDirResult{}, fs.ErrNotExist
		}
		return child.ReadDir(childReq)
	}
	nodes := make([]Node, 0, len(p.children))
	for _, child := range p.children {
		childName := child.providerName()
		if childName == "" {
			continue
		}
		childReq := req
		childReq.TargetPath = joinPath(req.TargetPath, childName)
		childReq.TargetRel = childName
		result, err := child.Resolve(childReq)
		if err != nil {
			if err == fs.ErrNotExist {
				continue
			}
			return ReadDirResult{}, err
		}
		nodes = append(nodes, result.Node)
	}
	sortNodes(nodes)
	return ReadDirResult{
		Entries:  nodes,
		Cache:    cacheHint(0, req.TargetPath, nil, []string{req.Route.ID}, false),
		Complete: true,
	}, nil
}

func (p staticDirProvider) localRel(targetRel string) (string, bool) {
	if p.name == "" {
		return targetRel, true
	}
	if targetRel == p.name {
		return ".", true
	}
	prefix := p.name + "/"
	if strings.HasPrefix(targetRel, prefix) {
		return strings.TrimPrefix(targetRel, prefix), true
	}
	return "", false
}

func (p staticDirProvider) childRequest(req Request, localRel string) (namedProvider, Request, bool) {
	first, rest, ok := splitFirst(localRel)
	if !ok {
		return nil, Request{}, false
	}
	for _, child := range p.children {
		if child.providerName() != first {
			continue
		}
		childReq := req
		childReq.TargetRel = first
		childReq.TargetPath = joinPath(parentPath(req.TargetPath), first)
		if rest != "." {
			childReq.TargetRel = joinPath(first, rest)
			childReq.TargetPath = req.TargetPath
		}
		return child, childReq, true
	}
	return nil, Request{}, false
}

type staticFileProvider struct {
	name    string
	data    []byte
	mode    fs.FileMode
	modTime time.Time
}

func StaticFile(name string, data []byte) VirtualProvider {
	return staticFileProvider{name: name, data: append([]byte(nil), data...), mode: 0444}
}

func (p staticFileProvider) providerName() string {
	return p.name
}

func (p staticFileProvider) Resolve(req Request) (ResolveResult, error) {
	if req.TargetRel != p.name {
		return ResolveResult{}, fs.ErrNotExist
	}
	node := Node{
		TargetPath: req.TargetPath,
		Kind:       NodeFile,
		Mode:       p.mode,
		Size:       int64(len(p.data)),
		ModTime:    p.modTime,
		Virtual:    true,
		Opener: staticOpener{
			name:    path.Base(req.TargetPath),
			data:    p.data,
			mode:    p.mode,
			modTime: p.modTime,
		},
	}
	decision, err := applyFilter(req.Filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
	if err != nil {
		return ResolveResult{}, err
	}
	if decision != FilterInclude {
		return ResolveResult{}, fs.ErrNotExist
	}
	return ResolveResult{Node: node, Cache: cacheHint(0, req.TargetPath, nil, []string{req.Route.ID}, false)}, nil
}

func (p staticFileProvider) ReadDir(Request) (ReadDirResult, error) {
	return ReadDirResult{}, fs.ErrNotExist
}

type staticOpener struct {
	name    string
	data    []byte
	mode    fs.FileMode
	modTime time.Time
}

func (o staticOpener) Open(OpenRequest) (fs.File, error) {
	return newBytesFile(o.name, o.data, o.mode, o.modTime), nil
}

type generatedFileProvider struct {
	name string
	fn   func(VirtualContext) ([]byte, error)
}

func TextFile(name string, fn func(VirtualContext) (string, error)) VirtualProvider {
	return generatedFileProvider{
		name: name,
		fn: func(ctx VirtualContext) ([]byte, error) {
			text, err := fn(ctx)
			if err != nil {
				return nil, err
			}
			return []byte(text), nil
		},
	}
}

func JSONFile(name string, fn func(VirtualContext) (any, error)) VirtualProvider {
	return generatedFileProvider{
		name: name,
		fn: func(ctx VirtualContext) ([]byte, error) {
			value, err := fn(ctx)
			if err != nil {
				return nil, err
			}
			data, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return nil, err
			}
			return append(data, '\n'), nil
		},
	}
}

func (p generatedFileProvider) providerName() string {
	return p.name
}

func (p generatedFileProvider) Resolve(req Request) (ResolveResult, error) {
	if req.TargetRel != p.name {
		return ResolveResult{}, fs.ErrNotExist
	}
	node := Node{
		TargetPath: req.TargetPath,
		Kind:       NodeFile,
		Mode:       0444,
		Virtual:    true,
		Opener: generatedOpener{
			name: path.Base(req.TargetPath),
			fn:   p.fn,
			ctx:  contextFromRequest(req),
		},
	}
	decision, err := applyFilter(req.Filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
	if err != nil {
		return ResolveResult{}, err
	}
	if decision != FilterInclude {
		return ResolveResult{}, fs.ErrNotExist
	}
	return ResolveResult{Node: node, Cache: cacheHint(0, req.TargetPath, nil, []string{req.Route.ID}, false)}, nil
}

func (p generatedFileProvider) ReadDir(Request) (ReadDirResult, error) {
	return ReadDirResult{}, fs.ErrNotExist
}

type generatedOpener struct {
	name string
	fn   func(VirtualContext) ([]byte, error)
	ctx  VirtualContext
}

func (o generatedOpener) Open(OpenRequest) (fs.File, error) {
	data, err := o.fn(o.ctx)
	if err != nil {
		return nil, err
	}
	return newBytesFile(o.name, data, 0444, time.Time{}), nil
}

func contextFromRequest(req Request) VirtualContext {
	return VirtualContext{
		Source:     req.Source,
		TargetPath: req.TargetPath,
		TargetRel:  req.TargetRel,
		TargetRoot: req.TargetRoot,
		SourceRoot: req.SourceRoot,
		Route:      req.Route,
		Params:     req.Params.Clone(),
	}
}

func splitFirst(name string) (first, rest string, ok bool) {
	name = strings.Trim(name, "/")
	if name == "" || name == "." {
		return "", "", false
	}
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 1 {
		return parts[0], ".", true
	}
	return parts[0], parts[1], true
}
