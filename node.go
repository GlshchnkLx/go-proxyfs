package proxyfs

import (
	"io/fs"
	"time"
)

type Node struct {
	TargetPath string
	SourcePath string

	Kind    NodeKind
	Mode    fs.FileMode
	Size    int64
	ModTime time.Time

	Virtual bool
	Opener  Opener
}

type NodeKind uint8

const (
	NodeFile NodeKind = iota + 1
	NodeDir
)

type ConflictPolicy int

const (
	ConflictError ConflictPolicy = iota
	ConflictFirst
	ConflictLast
	ConflictRename
)

type Scope struct {
	TargetRoot  string
	SourceRoots []string
	RouteIDs    []string
	Recursive   bool
}
