package proxyfs_test

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/GlshchnkLx/go-proxyfs"
)

func TestFlatRoute(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"SKU-1/docs/manual.txt":          {Data: []byte("manual")},
		"SKU-1/images/body.png":          {Data: []byte("body")},
		"SKU-1/images/preview/small.png": {Data: []byte("small")},
	}, proxyfs.NewRouteSet().
		Handle("/", proxyfs.Identity()).
		Handle("/{root}/**", proxyfs.Flat(proxyfs.FlatConfig{
			MaxDepth:       4,
			ConflictPolicy: proxyfs.ConflictError,
		})),
		proxyfs.AllowAll(),
	)

	if names := mustReadDirNames(t, pfs, "SKU-1"); !slices.Equal(names, []string{"body.png", "manual.txt", "small.png"}) {
		t.Fatalf("unexpected flat listing: %v", names)
	}
	data, err := fs.ReadFile(pfs, "SKU-1/manual.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "manual" {
		t.Fatalf("unexpected file data: %q", data)
	}
	sourcePaths, err := pfs.SourcePaths("SKU-1/body.png")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sourcePaths, []string{"SKU-1/images/body.png"}) {
		t.Fatalf("unexpected source mapping: %v", sourcePaths)
	}
	targetPaths, err := pfs.TargetPaths("SKU-1/images/preview/small.png")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(targetPaths, []string{"SKU-1/small.png"}) {
		t.Fatalf("unexpected target mapping: %v", targetPaths)
	}
}

func TestFlatRoutePassesFSTest(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"SKU-1/docs/manual.txt": {Data: []byte("manual")},
		"SKU-1/images/body.png": {Data: []byte("body")},
	}, proxyfs.NewRouteSet().
		Handle("/", proxyfs.Identity()).
		Handle("/{root}/**", proxyfs.Flat(proxyfs.FlatConfig{
			MaxDepth:       4,
			ConflictPolicy: proxyfs.ConflictError,
		})),
		proxyfs.AllowAll(),
	)

	if err := fstest.TestFS(pfs, "SKU-1/manual.txt", "SKU-1/body.png"); err != nil {
		t.Fatal(err)
	}
}

func TestFlatConflictRename(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"SKU-1/a/readme.txt": {Data: []byte("first")},
		"SKU-1/b/readme.txt": {Data: []byte("second")},
	}, proxyfs.NewRouteSet().
		Handle("/", proxyfs.Identity()).
		Handle("/{root}/**", proxyfs.Flat(proxyfs.FlatConfig{
			ConflictPolicy: proxyfs.ConflictRename,
		})),
		proxyfs.AllowAll(),
	)

	if names := mustReadDirNames(t, pfs, "SKU-1"); !slices.Equal(names, []string{"readme.txt", "readme_2.txt"}) {
		t.Fatalf("unexpected renamed entries: %v", names)
	}
}

func TestClassifierRoute(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"SKU-1/docs/manual.txt": {Data: []byte("manual")},
		"SKU-1/images/body.png": {Data: []byte("body")},
		"SKU-1/spec.pdf":        {Data: []byte("spec")},
		"SKU-1/blob.bin":        {Data: []byte("blob")},
	}, proxyfs.NewRouteSet().
		Handle("/", proxyfs.Identity()).
		Handle("/{root}/**", proxyfs.Classifier(proxyfs.ClassifierConfig{
			MaxDepth:       4,
			ConflictPolicy: proxyfs.ConflictError,
		})),
		proxyfs.AllowAll(),
	)

	if names := mustReadDirNames(t, pfs, "SKU-1"); !slices.Equal(names, []string{"Document", "Image", "Other", "Text"}) {
		t.Fatalf("unexpected category listing: %v", names)
	}
	if names := mustReadDirNames(t, pfs, "SKU-1/Text"); !slices.Equal(names, []string{"manual.txt"}) {
		t.Fatalf("unexpected text listing: %v", names)
	}
	data, err := fs.ReadFile(pfs, "SKU-1/Image/body.png")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "body" {
		t.Fatalf("unexpected file data: %q", data)
	}
	sourcePaths, err := pfs.SourcePaths("SKU-1/Document/spec.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sourcePaths, []string{"SKU-1/spec.pdf"}) {
		t.Fatalf("unexpected source mapping: %v", sourcePaths)
	}
	targetPaths, err := pfs.TargetPaths("SKU-1/docs/manual.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(targetPaths, []string{"SKU-1/Text/manual.txt"}) {
		t.Fatalf("unexpected target mapping: %v", targetPaths)
	}
}

func TestClassifierRoutePassesFSTest(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"SKU-1/docs/manual.txt": {Data: []byte("manual")},
		"SKU-1/images/body.png": {Data: []byte("body")},
	}, proxyfs.NewRouteSet().
		Handle("/", proxyfs.Identity()).
		Handle("/{root}/**", proxyfs.Classifier(proxyfs.ClassifierConfig{
			MaxDepth:       4,
			ConflictPolicy: proxyfs.ConflictError,
		})),
		proxyfs.AllowAll(),
	)

	if err := fstest.TestFS(pfs, "SKU-1/Text/manual.txt", "SKU-1/Image/body.png"); err != nil {
		t.Fatal(err)
	}
}

func TestRouteSpecificityAndSourceTemplateParams(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"docs/a.txt":              {Data: []byte("wrong")},
		"assets/a.txt":            {Data: []byte("asset")},
		"catalog/SKU-1/raw/b.txt": {Data: []byte("raw")},
	}, proxyfs.NewRouteSet().
		Handle("/**", proxyfs.Identity()).
		Handle("/docs/**", proxyfs.Source("assets"), proxyfs.Identity()).
		Handle("/products/{sku}/raw/**", proxyfs.Source("catalog/{sku}/raw"), proxyfs.Identity()),
		proxyfs.AllowAll(),
	)

	data, err := fs.ReadFile(pfs, "docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "asset" {
		t.Fatalf("more specific route did not win: %q", data)
	}
	data, err = fs.ReadFile(pfs, "products/SKU-1/raw/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "raw" {
		t.Fatalf("source template params were not applied: %q", data)
	}
}

func TestRouterSynthesizesVirtualParentDirectories(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"products/SKU-1/images/body.png": {Data: []byte("body")},
		"products/SKU-1/docs/manual.txt": {Data: []byte("manual")},
	}, proxyfs.NewRouteSet().
		Handle("/products/{sku}/Image/**", proxyfs.Source("products/{sku}/images"), proxyfs.Identity()).
		Handle("/products/{sku}/Text/**", proxyfs.Source("products/{sku}/docs"), proxyfs.Identity()),
		proxyfs.AllowAll(),
	)

	if names := mustReadDirNames(t, pfs, "products/SKU-1"); !slices.Equal(names, []string{"Image", "Text"}) {
		t.Fatalf("unexpected synthesized route children: %v", names)
	}
	if names := mustReadDirNames(t, pfs, "products/SKU-1/Image"); !slices.Equal(names, []string{"body.png"}) {
		t.Fatalf("unexpected image route entries: %v", names)
	}
}

func TestVirtualMetaFile(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"products/SKU-1/manual.txt": {Data: []byte("manual")},
	}, proxyfs.NewRouteSet().
		Handle("/products/{sku}/**", proxyfs.Source("products/{sku}"), proxyfs.Identity()).
		Handle("/products/{sku}/.meta/**", proxyfs.Source("products/{sku}"), proxyfs.Virtual(proxyfs.StaticDir(
			proxyfs.JSONFile("info.json", func(ctx proxyfs.VirtualContext) (any, error) {
				return map[string]string{"sku": ctx.Params["sku"]}, nil
			}),
		))),
		proxyfs.AllowAll(),
	)

	if names := mustReadDirNames(t, pfs, "products/SKU-1"); !slices.Equal(names, []string{".meta", "manual.txt"}) {
		t.Fatalf("unexpected product entries: %v", names)
	}
	if names := mustReadDirNames(t, pfs, "products/SKU-1/.meta"); !slices.Equal(names, []string{"info.json"}) {
		t.Fatalf("unexpected meta entries: %v", names)
	}
	data, err := fs.ReadFile(pfs, "products/SKU-1/.meta/info.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"sku": "SKU-1"`) {
		t.Fatalf("unexpected generated JSON: %s", data)
	}
}

func TestRouteOpenFilterCanDenyFile(t *testing.T) {
	pfs := newRoutedFS(t, fstest.MapFS{
		"public.txt": {Data: []byte("ok")},
		"secret.txt": {Data: []byte("secret")},
	}, proxyfs.NewRouteSet().Handle("/**", proxyfs.Identity()),
		openDenyFilter("secret.txt"),
	)

	if _, err := fs.Stat(pfs, "secret.txt"); err != nil {
		t.Fatalf("stat should remain visible before open denial: %v", err)
	}
	if _, err := fs.ReadFile(pfs, "secret.txt"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected permission error, got %v", err)
	}
}

func TestAmbiguousRoutesFailBuild(t *testing.T) {
	_, err := proxyfs.New(proxyfs.Config{
		Source: fstest.MapFS{},
		Routes: proxyfs.NewRouteSet().
			Handle("/{left}/**", proxyfs.Identity()).
			Handle("/{right}/**", proxyfs.Identity()),
	})
	if err == nil {
		t.Fatal("expected ambiguous routes to fail")
	}
}

func TestNegativeCacheAvoidsRepeatedMisses(t *testing.T) {
	handler := &countingMissingHandler{}
	pfs := newRoutedFS(t, fstest.MapFS{}, proxyfs.NewRouteSet().Handle("/**", handler), proxyfs.AllowAll())

	for i := 0; i < 2; i++ {
		if _, err := fs.Stat(pfs, "missing.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("expected ErrNotExist, got %v", err)
		}
	}
	if handler.resolveCount != 1 {
		t.Fatalf("expected one handler miss due to negative cache, got %d", handler.resolveCount)
	}
}

func TestMemoryCacheClonesSlicesAndInvalidatesRecursive(t *testing.T) {
	cache := proxyfs.NewMemoryCache()
	key := proxyfs.CacheKey{TargetPath: "a", RouteID: "r1", Kind: proxyfs.CacheDir}
	if err := cache.Put(proxyfs.CacheEntry{
		Key:         key,
		Children:    []proxyfs.Node{{TargetPath: "a/file.txt", Kind: proxyfs.NodeFile}},
		SourcePaths: []string{"src/a"},
		TargetPaths: []string{"a"},
		Scope:       proxyfs.Scope{TargetRoot: "a", SourceRoots: []string{"src/a"}, RouteIDs: []string{"r1"}},
	}); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := cache.Get(key)
	if err != nil || !ok {
		t.Fatalf("expected cache hit, ok=%v err=%v", ok, err)
	}
	entry.Children[0].TargetPath = "mutated"
	entry.SourcePaths[0] = "mutated"
	entry.TargetPaths[0] = "mutated"
	entry.Scope.SourceRoots[0] = "mutated"
	entry, ok, err = cache.Get(key)
	if err != nil || !ok {
		t.Fatalf("expected cache hit after mutation attempt, ok=%v err=%v", ok, err)
	}
	if entry.Children[0].TargetPath != "a/file.txt" || entry.SourcePaths[0] != "src/a" || entry.TargetPaths[0] != "a" || entry.Scope.SourceRoots[0] != "src/a" {
		t.Fatalf("cache entry was mutated through clone: %+v", entry)
	}

	childKey := proxyfs.CacheKey{TargetPath: "a/b", RouteID: "r1", Kind: proxyfs.CacheNode}
	otherKey := proxyfs.CacheKey{TargetPath: "c", RouteID: "r1", Kind: proxyfs.CacheNode}
	_ = cache.Put(proxyfs.CacheEntry{Key: childKey, Node: proxyfs.Node{TargetPath: "a/b", Kind: proxyfs.NodeFile}})
	_ = cache.Put(proxyfs.CacheEntry{Key: otherKey, Node: proxyfs.Node{TargetPath: "c", Kind: proxyfs.NodeFile}})
	if err := cache.Invalidate(proxyfs.Scope{TargetRoot: "a", Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := cache.Get(key); ok {
		t.Fatal("expected root entry to be invalidated")
	}
	if _, ok, _ := cache.Get(childKey); ok {
		t.Fatal("expected descendant entry to be invalidated")
	}
	if _, ok, _ := cache.Get(otherKey); !ok {
		t.Fatal("expected unrelated entry to remain")
	}
}

type openDenyFilter string

func (f openDenyFilter) Apply(entry proxyfs.FilterEntry) (proxyfs.FilterDecision, error) {
	if entry.Phase == proxyfs.FilterOpen && entry.Name == string(f) {
		return proxyfs.FilterDeny, nil
	}
	return proxyfs.FilterInclude, nil
}

type countingMissingHandler struct {
	resolveCount int
}

func (h *countingMissingHandler) Resolve(proxyfs.Request) (proxyfs.ResolveResult, error) {
	h.resolveCount++
	return proxyfs.ResolveResult{}, fs.ErrNotExist
}

func (h *countingMissingHandler) ReadDir(proxyfs.Request) (proxyfs.ReadDirResult, error) {
	return proxyfs.ReadDirResult{}, fs.ErrNotExist
}

func (h *countingMissingHandler) SourcePaths(proxyfs.TargetRequest) (proxyfs.SourcePathsResult, error) {
	return proxyfs.SourcePathsResult{}, fs.ErrNotExist
}

func (h *countingMissingHandler) TargetPaths(proxyfs.SourceRequest) (proxyfs.TargetPathsResult, error) {
	return proxyfs.TargetPathsResult{}, fs.ErrNotExist
}

func newRoutedFS(t *testing.T, source fs.FS, routes *proxyfs.RouteSet, filter proxyfs.Filter) *proxyfs.FS {
	t.Helper()
	pfs, err := proxyfs.New(proxyfs.Config{
		Source:      source,
		Routes:      routes,
		Filter:      filter,
		DefaultTTL:  time.Minute,
		NegativeTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pfs
}
