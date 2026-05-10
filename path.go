package proxyfs

import (
	"io/fs"
	"path"
	"strings"
)

func cleanTargetPath(name string) (string, error) {
	return CleanPath(name)
}

func CleanPath(name string) (string, error) {
	if name == "." {
		return ".", nil
	}
	if name == "" || strings.HasPrefix(name, "/") || !fs.ValidPath(name) {
		return "", fs.ErrInvalid
	}
	return path.Clean(name), nil
}

func pathDepth(name string) int {
	return PathDepth(name)
}

func PathDepth(name string) int {
	if name == "." || name == "" {
		return 0
	}
	return strings.Count(name, "/") + 1
}

func parentPath(name string) string {
	return ParentPath(name)
}

func ParentPath(name string) string {
	if name == "." {
		return "."
	}
	parent := path.Dir(name)
	if parent == "" {
		return "."
	}
	return parent
}

func joinPath(elem ...string) string {
	return JoinPath(elem...)
}

func JoinPath(elem ...string) string {
	parts := make([]string, 0, len(elem))
	for _, part := range elem {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "."
	}
	return path.Join(parts...)
}

func relPath(root, name string) (string, bool) {
	return RelPath(root, name)
}

func RelPath(root, name string) (string, bool) {
	root = cleanRoot(root)
	name = cleanRoot(name)
	if root == "." {
		return name, true
	}
	if name == root {
		return ".", true
	}
	if strings.HasPrefix(name, root+"/") {
		return strings.TrimPrefix(name, root+"/"), true
	}
	return "", false
}
