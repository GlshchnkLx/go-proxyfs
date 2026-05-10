# proxyfs

`proxyfs` is a read-only virtual filesystem for Go. It exposes a standard
`io/fs` filesystem while lazily mapping target paths to another source
filesystem, route handlers, or generated virtual files.

It is useful when an application needs to present a clean, stable, or
domain-specific directory layout over a different physical layout, including
remote filesystems.

## Features

- Implements `fs.FS`, `fs.ReadDirFS`, and `fs.StatFS`.
- Keeps public paths compatible with `io/fs` rules.
- Uses route-based mapping instead of one global indexer.
- Supports source-to-target and target-to-source reverse mapping.
- Provides built-in handlers:
  - `Identity`
  - `Mount`
  - `Flat`
  - `Classifier`
  - `Virtual`
  - `Transform`
- Supports generated virtual files through static, text, and JSON providers.
- Provides phase-aware filters for indexing, target visibility, and open
  permissions.
- Caches resolved nodes, directory listings, reverse mappings, and negative
  lookups.
- Allows explicit cache invalidation for external watchers or refresh logic.

## Installation

```sh
go get github.com/GlshchnkLx/go-proxyfs
```

Import:

```go
import "github.com/GlshchnkLx/go-proxyfs"
```

## Core Model

`proxyfs` separates three path concepts:

- **source path**: the real path inside the wrapped `fs.FS`;
- **target path**: the public path visible to callers;
- **route-relative path**: the part of a target path handled by a matched route.

Runtime flow:

```text
Open / Stat / ReadDir
  -> proxyfs.FS
  -> cache
  -> router
  -> route handler
  -> source fs.FS or virtual provider
```

All public paths are target paths. Source paths stay internal unless you
explicitly call reverse mapping helpers such as `SourcePaths` or `TargetPaths`.

## Basic Usage

The simplest setup exposes the source filesystem unchanged:

```go
package main

import (
	"io/fs"
	"log"
	"testing/fstest"

	"github.com/GlshchnkLx/go-proxyfs"
)

func main() {
	source := fstest.MapFS{
		"docs/manual.txt": {Data: []byte("manual")},
	}

	pfs, err := proxyfs.New(proxyfs.Config{
		Source: source,
		Routes: proxyfs.NewRouteSet().
			Handle("/**", proxyfs.Identity()),
	})
	if err != nil {
		log.Fatal(err)
	}

	data, err := fs.ReadFile(pfs, "docs/manual.txt")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%s", data)
}
```

If `Routes` is omitted, `proxyfs` installs the same catch-all identity route by
default.

## Routes

Routes decide which handler owns a target path.

```go
routes := proxyfs.NewRouteSet().
	Handle("/", proxyfs.Identity()).
	Handle("/products/{sku}/docs/**",
		proxyfs.Source("catalog/{sku}/documentation"),
		proxyfs.Identity(),
	)
```

Supported route pattern forms:

```text
/                       exact root
/docs                   exact path
/docs/**                subtree
/products/{sku}/**      named segment plus subtree
/products/*/raw/**      anonymous one-segment wildcard
/**                     catch-all
```

Pattern rules:

- `{name}` matches exactly one segment and stores it in route params.
- `*` matches exactly one unnamed segment.
- `**` matches zero or more segments and must be the last pattern segment.
- More specific routes win over broader routes.
- Ambiguous equal-specificity routes fail at construction time.

For this route:

```go
proxyfs.NewRouteSet().
	Handle("/products/{sku}/raw/**",
		proxyfs.Source("archive/{sku}/raw"),
		proxyfs.Identity(),
	)
```

Target path:

```text
products/SKU-1/raw/image.png
```

maps to source path:

```text
archive/SKU-1/raw/image.png
```

and the handler receives:

```text
Params["sku"] = "SKU-1"
TargetRoot    = "products/SKU-1/raw"
TargetRel     = "image.png"
SourceRoot    = "archive/SKU-1/raw"
```

## Built-In Handlers

### Identity

Maps target relative paths directly to source relative paths.

```go
routes := proxyfs.NewRouteSet().
	Handle("/public/**",
		proxyfs.Source("assets/public"),
		proxyfs.Identity(),
	)
```

`public/logo.png` reads `assets/public/logo.png`.

### Mount

`Mount` is a convenience identity-style handler with a fixed source root.

```go
routes := proxyfs.NewRouteSet().
	Handle("/public/**", proxyfs.Mount("assets/public"))
```

### Flat

`Flat` walks a source subtree and exposes files directly under the route target
root.

```go
routes := proxyfs.NewRouteSet().
	Handle("/{sku}/**", proxyfs.Flat(proxyfs.FlatConfig{
		MaxDepth:       8,
		ConflictPolicy: proxyfs.ConflictRename,
	}))
```

Source:

```text
SKU-1/docs/manual.txt
SKU-1/images/body.png
```

Target:

```text
SKU-1/manual.txt
SKU-1/body.png
```

### Classifier

`Classifier` groups files by extension categories.

```go
routes := proxyfs.NewRouteSet().
	Handle("/{sku}/**", proxyfs.Classifier(proxyfs.ClassifierConfig{
		MaxDepth: 8,
		Categories: map[string]string{
			".txt": "Text",
			".md":  "Text",
			".png": "Image",
			".pdf": "Document",
		},
		Fallback:       "Other",
		ConflictPolicy: proxyfs.ConflictRename,
	}))
```

Source:

```text
SKU-1/docs/manual.txt
SKU-1/images/body.png
```

Target:

```text
SKU-1/Text/manual.txt
SKU-1/Image/body.png
```

### Virtual

`Virtual` exposes files that do not exist in the source filesystem.

```go
routes := proxyfs.NewRouteSet().
	Handle("/products/{sku}/**",
		proxyfs.Source("products/{sku}"),
		proxyfs.Identity(),
	).
	Handle("/products/{sku}/.meta/**",
		proxyfs.Virtual(proxyfs.StaticDir(
			proxyfs.JSONFile("info.json", func(ctx proxyfs.VirtualContext) (any, error) {
				return map[string]string{
					"sku": ctx.Params["sku"],
				}, nil
			}),
		)),
	)
```

Reading:

```text
products/SKU-1/.meta/info.json
```

returns generated JSON.

### Transform

`Transform` is an escape hatch for custom materialized mappings without
implementing the entire `Handler` interface.

```go
routes := proxyfs.NewRouteSet().
	Handle("/files/**", proxyfs.Transform(proxyfs.TransformConfig{
		MaxDepth:       8,
		ConflictPolicy: proxyfs.ConflictRename,
		MapFile: func(ctx proxyfs.MapContext, entry proxyfs.SourceEntry) ([]proxyfs.Node, error) {
			node := proxyfs.NodeFromFileInfo(
				proxyfs.JoinPath(entry.TargetRoot, entry.SourceRel),
				entry.SourcePath,
				entry.Info,
			)
			return []proxyfs.Node{node}, nil
		},
	}))
```

## Filters

Filters are phase-aware:

```go
type Filter interface {
	Apply(entry proxyfs.FilterEntry) (proxyfs.FilterDecision, error)
}
```

Filter phases:

- `FilterIndex`: source traversal and materialization;
- `FilterTarget`: target visibility for `Resolve`, `Stat`, and `ReadDir`;
- `FilterOpen`: open-time permission.

Built-in filters:

```go
proxyfs.AllowAll()
proxyfs.HideDotFiles()
proxyfs.MaxDepth(3)
proxyfs.NamePattern("*.md")
proxyfs.ExtAllowList(".txt", ".md")
proxyfs.ExtDenyList(".tmp")
proxyfs.And(...)
proxyfs.Or(...)
```

Example:

```go
pfs, err := proxyfs.New(proxyfs.Config{
	Source: source,
	Routes: proxyfs.NewRouteSet().
		Handle("/**", proxyfs.Identity()),
	Filter: proxyfs.And(
		proxyfs.HideDotFiles(),
		proxyfs.ExtDenyList(".tmp"),
	),
})
```

Open-time denial:

```go
denySecret := proxyfs.FilterFunc(func(entry proxyfs.FilterEntry) (proxyfs.FilterDecision, error) {
	if entry.Phase == proxyfs.FilterOpen && entry.Name == "secret.txt" {
		return proxyfs.FilterDeny, nil
	}
	return proxyfs.FilterInclude, nil
})
```

`FilterDeny` in `FilterOpen` returns `fs.ErrPermission` from `Open` while still
allowing a path to be visible to `Stat` or `ReadDir`.

## Reverse Mapping

`proxyfs` can map in both directions:

```go
sourcePaths, err := pfs.SourcePaths("SKU-1/Image/body.png")
targetPaths, err := pfs.TargetPaths("SKU-1/images/body.png")
```

Results are deduplicated and sorted.

Virtual providers usually return no source paths unless they deliberately set a
source path on the generated node.

## Cache and Invalidation

If no cache is provided, `proxyfs` uses an in-memory cache:

```go
pfs, err := proxyfs.New(proxyfs.Config{
	Source: source,
	Cache:  proxyfs.NewMemoryCache(),
})
```

Public invalidation helpers:

```go
err := pfs.Invalidate(proxyfs.Scope{
	TargetRoot: "products/SKU-1",
	Recursive:  true,
})

err = pfs.InvalidatePath("products/SKU-1/manual.txt")
err = pfs.ClearCache()
```

`InvalidatePath` invalidates the target path and its parent listing, which is
usually what callers want after a single file changes.

## Custom Handlers

Implement `proxyfs.Handler` when route behavior cannot be expressed with the
built-in handlers.

```go
type MyHandler struct{}

func (MyHandler) Resolve(req proxyfs.Request) (proxyfs.ResolveResult, error) {
	// Return one target node or fs.ErrNotExist.
	panic("implement me")
}

func (MyHandler) ReadDir(req proxyfs.Request) (proxyfs.ReadDirResult, error) {
	// Return direct target children.
	panic("implement me")
}

func (MyHandler) SourcePaths(req proxyfs.TargetRequest) (proxyfs.SourcePathsResult, error) {
	panic("implement me")
}

func (MyHandler) TargetPaths(req proxyfs.SourceRequest) (proxyfs.TargetPathsResult, error) {
	panic("implement me")
}
```

Useful helpers for custom handlers:

```go
proxyfs.CleanPath(name)
proxyfs.JoinPath(parts...)
proxyfs.ParentPath(name)
proxyfs.PathDepth(name)
proxyfs.RelPath(root, name)
proxyfs.NodeFromFileInfo(targetPath, sourcePath, info)
proxyfs.FilterFunc(...)
proxyfs.OpenerFunc(...)
```

## Error Behavior

Public filesystem methods return `*fs.PathError` where appropriate. `errors.Is`
continues to work for:

- `fs.ErrNotExist`
- `fs.ErrInvalid`
- `fs.ErrPermission`
- `proxyfs.ErrConflict`

## Notes

- The package is read-only through the standard `io/fs` interfaces.
- It uses `/` paths only and does not use OS-specific separators.
- Route and handler behavior is lazy; `proxyfs` does not build a full global
  index at startup.
- For external source watchers, call `Invalidate`, `InvalidatePath`, or
  `ClearCache` from watcher events.
