package proxyfs_test

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/GlshchnkLx/go-proxyfs"
)

func TestIdentityRoutePassesFSTest(t *testing.T) {
	pfs := newTestFS(t, fstest.MapFS{
		"alpha.txt":       {Data: []byte("alpha")},
		"docs/manual.txt": {Data: []byte("manual")},
	}, proxyfs.AllowAll())

	if err := fstest.TestFS(pfs, "alpha.txt", "docs/manual.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestHideDotFiles(t *testing.T) {
	pfs := newTestFS(t, fstest.MapFS{
		"docs/manual.txt": {Data: []byte("manual")},
		"docs/.draft.txt": {Data: []byte("draft")},
	}, proxyfs.HideDotFiles())

	entries, err := fs.ReadDir(pfs, "docs")
	if err != nil {
		t.Fatal(err)
	}
	names := entryNames(entries)
	if !slices.Equal(names, []string{"manual.txt"}) {
		t.Fatalf("unexpected entries: %v", names)
	}
	if _, err := fs.Stat(pfs, "docs/.draft.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected ErrNotExist for hidden file, got %v", err)
	}
}

func TestExtAllowListKeepsDirectoriesForTraversal(t *testing.T) {
	pfs := newTestFS(t, fstest.MapFS{
		"docs/manual.txt": {Data: []byte("manual")},
		"docs/image.png":  {Data: []byte("image")},
		"notes/todo.txt":  {Data: []byte("todo")},
	}, proxyfs.ExtAllowList(".txt"))

	if names := mustReadDirNames(t, pfs, "."); !slices.Equal(names, []string{"docs", "notes"}) {
		t.Fatalf("directories should remain visible for traversal: %v", names)
	}
	if names := mustReadDirNames(t, pfs, "docs"); !slices.Equal(names, []string{"manual.txt"}) {
		t.Fatalf("unexpected filtered entries: %v", names)
	}
}

func TestNamePattern(t *testing.T) {
	pfs := newTestFS(t, fstest.MapFS{
		"docs/manual.txt": {Data: []byte("manual")},
		"docs/readme.md":  {Data: []byte("readme")},
	}, proxyfs.NamePattern("*.md"))

	if names := mustReadDirNames(t, pfs, "docs"); !slices.Equal(names, []string{"readme.md"}) {
		t.Fatalf("unexpected pattern filtered entries: %v", names)
	}
}

func TestInvalidatePathRefreshesDirectoryListing(t *testing.T) {
	source := fstest.MapFS{
		"docs/manual.txt": {Data: []byte("manual")},
	}
	pfs := newTestFS(t, source, proxyfs.AllowAll())

	if names := mustReadDirNames(t, pfs, "docs"); !slices.Equal(names, []string{"manual.txt"}) {
		t.Fatalf("unexpected initial entries: %v", names)
	}
	source["docs/new.txt"] = &fstest.MapFile{Data: []byte("new")}
	if names := mustReadDirNames(t, pfs, "docs"); !slices.Equal(names, []string{"manual.txt"}) {
		t.Fatalf("cache should keep stale listing before invalidation: %v", names)
	}
	if err := pfs.InvalidatePath("docs"); err != nil {
		t.Fatal(err)
	}
	if names := mustReadDirNames(t, pfs, "docs"); !slices.Equal(names, []string{"manual.txt", "new.txt"}) {
		t.Fatalf("expected refreshed entries: %v", names)
	}
}

func TestInvalidPathReturnsPathError(t *testing.T) {
	pfs := newTestFS(t, fstest.MapFS{}, proxyfs.AllowAll())

	_, err := pfs.Open("../escape")
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("expected *fs.PathError, got %T: %v", err, err)
	}
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func newTestFS(t *testing.T, source fs.FS, filter proxyfs.Filter) *proxyfs.FS {
	t.Helper()
	pfs, err := proxyfs.New(proxyfs.Config{
		Source:      source,
		Routes:      proxyfs.NewRouteSet().Handle("/**", proxyfs.Identity()),
		Filter:      filter,
		DefaultTTL:  time.Minute,
		NegativeTTL: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pfs
}

func mustReadDirNames(t *testing.T, fsys fs.FS, dir string) []string {
	t.Helper()
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		t.Fatal(err)
	}
	return entryNames(entries)
}

func entryNames(entries []fs.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
