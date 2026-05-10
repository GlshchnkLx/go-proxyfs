package proxyfs

import (
	"io/fs"
)

type Materializer struct {
	MaxDepth       int
	ConflictPolicy ConflictPolicy
	Mapper         Mapper
	Filter         Filter
}

type Mapper interface {
	Map(entry SourceEntry) ([]Node, error)
}

type SourceEntry struct {
	SourcePath string
	SourceRel  string
	TargetRoot string
	Info       fs.FileInfo
	Params     Params
}

func (m Materializer) Materialize(req Request) ([]Node, error) {
	if m.Mapper == nil {
		return nil, nil
	}
	info, err := fs.Stat(req.Source, req.SourceRoot)
	if err != nil {
		return nil, unwrapPathErr(err)
	}
	if !info.IsDir() {
		return nil, fs.ErrInvalid
	}
	state := materializeState{
		nodes:  make(map[string]Node),
		counts: make(map[string]int),
		policy: m.ConflictPolicy,
	}
	filter := And(req.Filter, m.Filter)
	if err := m.walk(req, filter, req.SourceRoot, ".", 0, &state); err != nil {
		return nil, err
	}
	nodes := make([]Node, 0, len(state.nodes))
	for _, node := range state.nodes {
		nodes = append(nodes, node)
	}
	sortNodes(nodes)
	return nodes, nil
}

func (m Materializer) walk(req Request, filter Filter, sourceDir, sourceRel string, depth int, state *materializeState) error {
	entries, err := fs.ReadDir(req.Source, sourceDir)
	if err != nil {
		return unwrapPathErr(err)
	}
	for _, entry := range entries {
		nextDepth := depth + 1
		if m.MaxDepth > 0 && nextDepth > m.MaxDepth {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return unwrapPathErr(err)
		}
		sourcePath := joinPath(sourceDir, entry.Name())
		nextRel := joinPath(sourceRel, entry.Name())
		indexEntry := filterEntry(FilterIndex, sourcePath, joinPath(req.TargetRoot, nextRel), info, req.Route, req.Params)
		indexDecision, err := applyFilter(filter, indexEntry)
		if err != nil {
			return err
		}
		if indexDecision == FilterExcludeTree || indexDecision == FilterDeny {
			continue
		}

		mapped, err := m.Mapper.Map(SourceEntry{
			SourcePath: sourcePath,
			SourceRel:  nextRel,
			TargetRoot: req.TargetRoot,
			Info:       info,
			Params:     req.Params.Clone(),
		})
		if err != nil {
			return err
		}
		for _, node := range mapped {
			decision, err := applyFilter(filter, filterEntryForNode(FilterTarget, node, req.Route, req.Params))
			if err != nil {
				return err
			}
			if decision != FilterInclude {
				continue
			}
			if err := state.add(node); err != nil {
				return err
			}
		}

		if entry.IsDir() {
			if err := m.walk(req, filter, sourcePath, nextRel, nextDepth, state); err != nil {
				return err
			}
		}
	}
	return nil
}

type materializeState struct {
	nodes  map[string]Node
	counts map[string]int
	policy ConflictPolicy
}

func (s *materializeState) add(node Node) error {
	switch s.policy {
	case ConflictFirst:
		if _, exists := s.nodes[node.TargetPath]; !exists {
			s.nodes[node.TargetPath] = node
		}
	case ConflictLast:
		s.nodes[node.TargetPath] = node
	case ConflictRename:
		node.TargetPath = uniqueTargetPath(s.nodes, s.counts, node.TargetPath)
		s.nodes[node.TargetPath] = node
	default:
		if _, exists := s.nodes[node.TargetPath]; exists {
			return ErrConflict
		}
		s.nodes[node.TargetPath] = node
	}
	return nil
}
