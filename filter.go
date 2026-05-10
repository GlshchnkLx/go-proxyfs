package proxyfs

import (
	"io/fs"
	"path"
	"strings"
)

type Filter interface {
	Apply(entry FilterEntry) (FilterDecision, error)
}

type FilterPhase uint8

const (
	FilterIndex FilterPhase = iota + 1
	FilterTarget
	FilterOpen
)

type FilterEntry struct {
	Phase FilterPhase

	SourcePath string
	TargetPath string
	Name       string
	IsDir      bool
	Virtual    bool

	Depth int
	Info  fs.FileInfo
	Node  *Node

	Route  RouteInfo
	Params Params
}

type FilterDecision int

const (
	FilterInclude FilterDecision = iota
	FilterExclude
	FilterExcludeTree
	FilterDeny
)

type FilterFunc func(FilterEntry) (FilterDecision, error)

func (f FilterFunc) Apply(entry FilterEntry) (FilterDecision, error) {
	return f(entry)
}

func AllowAll() Filter {
	return FilterFunc(func(FilterEntry) (FilterDecision, error) {
		return FilterInclude, nil
	})
}

func HideDotFiles() Filter {
	return FilterFunc(func(entry FilterEntry) (FilterDecision, error) {
		if entry.Name != "." && strings.HasPrefix(entry.Name, ".") {
			if entry.IsDir && entry.Phase == FilterIndex {
				return FilterExcludeTree, nil
			}
			return FilterExclude, nil
		}
		return FilterInclude, nil
	})
}

func MaxDepth(maxDepth int) Filter {
	return FilterFunc(func(entry FilterEntry) (FilterDecision, error) {
		if maxDepth >= 0 && entry.Depth > maxDepth {
			if entry.IsDir && entry.Phase == FilterIndex {
				return FilterExcludeTree, nil
			}
			return FilterExclude, nil
		}
		return FilterInclude, nil
	})
}

func NamePattern(patterns ...string) Filter {
	return FilterFunc(func(entry FilterEntry) (FilterDecision, error) {
		if entry.IsDir || len(patterns) == 0 {
			return FilterInclude, nil
		}
		for _, pattern := range patterns {
			matched, err := path.Match(pattern, entry.Name)
			if err != nil {
				return FilterExclude, err
			}
			if matched {
				return FilterInclude, nil
			}
		}
		return FilterExclude, nil
	})
}

func ExtAllowList(exts ...string) Filter {
	allowed := extSet(exts)
	return FilterFunc(func(entry FilterEntry) (FilterDecision, error) {
		if entry.IsDir || len(allowed) == 0 {
			return FilterInclude, nil
		}
		if allowed[strings.ToLower(path.Ext(entry.Name))] {
			return FilterInclude, nil
		}
		return FilterExclude, nil
	})
}

func ExtDenyList(exts ...string) Filter {
	denied := extSet(exts)
	return FilterFunc(func(entry FilterEntry) (FilterDecision, error) {
		if entry.IsDir || len(denied) == 0 {
			return FilterInclude, nil
		}
		if denied[strings.ToLower(path.Ext(entry.Name))] {
			return FilterExclude, nil
		}
		return FilterInclude, nil
	})
}

func And(filters ...Filter) Filter {
	return FilterFunc(func(entry FilterEntry) (FilterDecision, error) {
		for _, filter := range filters {
			if filter == nil {
				continue
			}
			decision, err := filter.Apply(entry)
			if err != nil {
				return FilterExclude, err
			}
			if decision != FilterInclude {
				return decision, nil
			}
		}
		return FilterInclude, nil
	})
}

func Or(filters ...Filter) Filter {
	return FilterFunc(func(entry FilterEntry) (FilterDecision, error) {
		hasFilter := false
		denied := false
		for _, filter := range filters {
			if filter == nil {
				continue
			}
			hasFilter = true
			decision, err := filter.Apply(entry)
			if err != nil {
				return FilterExclude, err
			}
			if decision == FilterInclude {
				return FilterInclude, nil
			}
			if decision == FilterDeny {
				denied = true
			}
		}
		if !hasFilter {
			return FilterInclude, nil
		}
		if denied {
			return FilterDeny, nil
		}
		return FilterExclude, nil
	})
}

func filterEntry(phase FilterPhase, sourcePath, targetPath string, info fs.FileInfo, route RouteInfo, params Params) FilterEntry {
	name := path.Base(targetPath)
	if targetPath == "." {
		name = "."
	}
	return FilterEntry{
		Phase:      phase,
		SourcePath: sourcePath,
		TargetPath: targetPath,
		Name:       name,
		IsDir:      info.IsDir(),
		Depth:      pathDepth(targetPath),
		Info:       info,
		Route:      route,
		Params:     params.Clone(),
	}
}

func filterEntryForNode(phase FilterPhase, node Node, route RouteInfo, params Params) FilterEntry {
	name := path.Base(node.TargetPath)
	if node.TargetPath == "." {
		name = "."
	}
	return FilterEntry{
		Phase:      phase,
		SourcePath: node.SourcePath,
		TargetPath: node.TargetPath,
		Name:       name,
		IsDir:      node.Kind == NodeDir,
		Virtual:    node.Virtual,
		Depth:      pathDepth(node.TargetPath),
		Info:       nodeInfo{node: node},
		Node:       &node,
		Route:      route,
		Params:     params.Clone(),
	}
}

func applyFilter(filter Filter, entry FilterEntry) (FilterDecision, error) {
	if filter == nil {
		return FilterInclude, nil
	}
	return filter.Apply(entry)
}

func extSet(exts []string) map[string]bool {
	set := make(map[string]bool, len(exts))
	for _, ext := range exts {
		ext = strings.TrimSpace(strings.ToLower(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		set[ext] = true
	}
	return set
}
