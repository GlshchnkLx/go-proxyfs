package proxyfs

import (
	"io/fs"
	"time"
)

type Handler interface {
	Resolve(req Request) (ResolveResult, error)
	ReadDir(req Request) (ReadDirResult, error)
	SourcePaths(req TargetRequest) (SourcePathsResult, error)
	TargetPaths(req SourceRequest) (TargetPathsResult, error)
}

type Params map[string]string

func (p Params) Clone() Params {
	if len(p) == 0 {
		return nil
	}
	clone := make(Params, len(p))
	for key, value := range p {
		clone[key] = value
	}
	return clone
}

type RouteInfo struct {
	ID         string
	Pattern    string
	TargetRoot string
	SourceRoot string
	Priority   int
}

type Request struct {
	Source     fs.FS
	TargetPath string
	TargetRel  string

	SourceRoot string
	TargetRoot string

	Route  RouteInfo
	Params Params
	Filter Filter
}

type TargetRequest struct {
	Source     fs.FS
	TargetPath string
	TargetRel  string

	SourceRoot string
	TargetRoot string

	Route  RouteInfo
	Params Params
	Filter Filter
}

type SourceRequest struct {
	Source     fs.FS
	SourcePath string
	SourceRel  string

	SourceRoot string
	TargetRoot string

	Route  RouteInfo
	Params Params
	Filter Filter
}

type CacheHint struct {
	TTL       time.Duration
	Scope     Scope
	Cacheable bool
}

type ResolveResult struct {
	Node  Node
	Cache CacheHint
}

type ReadDirResult struct {
	Entries  []Node
	Cache    CacheHint
	Complete bool
}

type TargetPathsResult struct {
	TargetPaths []string
	Cache       CacheHint
}

type SourcePathsResult struct {
	SourcePaths []string
	Cache       CacheHint
}
