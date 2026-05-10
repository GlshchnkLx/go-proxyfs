package proxyfs

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

type Pattern struct {
	raw      string
	segments []patternSegment
	hasGlob  bool
}

type patternSegment struct {
	kind patternSegmentKind
	text string
}

type patternSegmentKind uint8

const (
	patternStatic patternSegmentKind = iota + 1
	patternNamed
	patternWildcard
	patternGlob
)

var patternNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ParsePattern(raw string) (Pattern, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Pattern{}, fmt.Errorf("proxyfs: route pattern is empty")
	}
	trimmed := strings.TrimPrefix(raw, "/")
	if trimmed == "" || trimmed == "." {
		return Pattern{raw: "/"}, nil
	}
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return Pattern{raw: "/"}, nil
	}
	parts := strings.Split(trimmed, "/")
	segments := make([]patternSegment, 0, len(parts))
	hasGlob := false
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return Pattern{}, fmt.Errorf("proxyfs: invalid route pattern segment %q in %q", part, raw)
		}
		switch {
		case part == "**":
			if i != len(parts)-1 {
				return Pattern{}, fmt.Errorf("proxyfs: ** is only allowed at the end in %q", raw)
			}
			hasGlob = true
			segments = append(segments, patternSegment{kind: patternGlob, text: part})
		case part == "*":
			segments = append(segments, patternSegment{kind: patternWildcard, text: part})
		case strings.HasPrefix(part, "{") || strings.HasSuffix(part, "}"):
			if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
				return Pattern{}, fmt.Errorf("proxyfs: invalid named segment %q in %q", part, raw)
			}
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			if !patternNameRE.MatchString(name) {
				return Pattern{}, fmt.Errorf("proxyfs: invalid named segment %q in %q", part, raw)
			}
			segments = append(segments, patternSegment{kind: patternNamed, text: name})
		default:
			if strings.ContainsAny(part, "{}*") {
				return Pattern{}, fmt.Errorf("proxyfs: invalid static segment %q in %q", part, raw)
			}
			segments = append(segments, patternSegment{kind: patternStatic, text: part})
		}
	}
	normalized := "/" + strings.Join(parts, "/")
	return Pattern{raw: normalized, segments: segments, hasGlob: hasGlob}, nil
}

func (p Pattern) String() string {
	if p.raw == "" {
		return "/"
	}
	return p.raw
}

func (p Pattern) Match(targetPath string) (PatternMatch, bool) {
	targetParts := splitCleanPath(targetPath)
	prefixLen := p.prefixLen()
	if p.hasGlob {
		if len(targetParts) < prefixLen {
			return PatternMatch{}, false
		}
	} else if len(targetParts) != len(p.segments) {
		return PatternMatch{}, false
	}

	params := make(Params)
	for i := 0; i < prefixLen; i++ {
		if !matchPatternSegment(p.segments[i], targetParts[i], params) {
			return PatternMatch{}, false
		}
	}
	if !p.hasGlob && len(targetParts) == 0 && len(params) == 0 {
		params = nil
	}
	targetRoot := joinPath(targetParts[:prefixLen]...)
	targetRel := "."
	if p.hasGlob {
		targetRel = joinPath(targetParts[prefixLen:]...)
	}
	return PatternMatch{
		Params:     params,
		TargetRoot: targetRoot,
		TargetRel:  targetRel,
	}, true
}

func (p Pattern) nextChild(parentPath string) (routeChild, bool) {
	parentParts := splitCleanPath(parentPath)
	prefixLen := p.prefixLen()
	if len(parentParts) >= prefixLen {
		return routeChild{}, false
	}
	params := make(Params)
	for i, part := range parentParts {
		if !matchPatternSegment(p.segments[i], part, params) {
			return routeChild{}, false
		}
	}
	next := p.segments[len(parentParts)]
	if next.kind != patternStatic {
		return routeChild{}, false
	}
	targetPath := joinPath(parentPath, next.text)
	return routeChild{
		name:       next.text,
		targetPath: targetPath,
		Params:     params,
	}, true
}

func (p Pattern) TargetRoot(params Params) (string, bool) {
	prefixLen := p.prefixLen()
	parts := make([]string, 0, prefixLen)
	for i := 0; i < prefixLen; i++ {
		segment := p.segments[i]
		switch segment.kind {
		case patternStatic:
			parts = append(parts, segment.text)
		case patternNamed:
			value := params[segment.text]
			if value == "" {
				return "", false
			}
			parts = append(parts, value)
		default:
			return "", false
		}
	}
	return joinPath(parts...), true
}

func (p Pattern) prefixLen() int {
	if p.hasGlob {
		return len(p.segments) - 1
	}
	return len(p.segments)
}

func (p Pattern) specificity() patternSpecificity {
	spec := patternSpecificity{
		exact:        !p.hasGlob,
		segmentCount: p.prefixLen(),
	}
	for _, segment := range p.segments {
		switch segment.kind {
		case patternStatic:
			spec.staticCount++
		case patternNamed:
			spec.namedCount++
		case patternWildcard:
			spec.wildcardCount++
		}
	}
	return spec
}

func (p Pattern) canOverlap(other Pattern) bool {
	i := 0
	for {
		if i >= len(p.segments) || i >= len(other.segments) {
			break
		}
		left := p.segments[i]
		right := other.segments[i]
		if left.kind == patternGlob || right.kind == patternGlob {
			return true
		}
		if left.kind == patternStatic && right.kind == patternStatic && left.text != right.text {
			return false
		}
		i++
	}
	if p.hasGlob || other.hasGlob {
		return true
	}
	return len(p.segments) == len(other.segments)
}

type patternSpecificity struct {
	exact         bool
	segmentCount  int
	staticCount   int
	namedCount    int
	wildcardCount int
}

func compareSpecificity(left, right patternSpecificity) int {
	if left.exact != right.exact {
		if left.exact {
			return 1
		}
		return -1
	}
	if left.segmentCount != right.segmentCount {
		if left.segmentCount > right.segmentCount {
			return 1
		}
		return -1
	}
	if left.staticCount != right.staticCount {
		if left.staticCount > right.staticCount {
			return 1
		}
		return -1
	}
	if left.namedCount != right.namedCount {
		if left.namedCount > right.namedCount {
			return 1
		}
		return -1
	}
	if left.wildcardCount != right.wildcardCount {
		if left.wildcardCount > right.wildcardCount {
			return 1
		}
		return -1
	}
	return 0
}

type PatternMatch struct {
	Params     Params
	TargetRoot string
	TargetRel  string
}

type routeChild struct {
	name       string
	targetPath string
	Params     Params
}

type SourceTemplate struct {
	raw      string
	segments []sourceSegment
}

type sourceSegment struct {
	named bool
	text  string
}

func ParseSourceTemplate(raw string) (SourceTemplate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" || raw == "." {
		return SourceTemplate{raw: "."}, nil
	}
	trimmed := strings.Trim(strings.TrimPrefix(raw, "/"), "/")
	parts := strings.Split(trimmed, "/")
	segments := make([]sourceSegment, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part == "*" || part == "**" {
			return SourceTemplate{}, fmt.Errorf("proxyfs: invalid source template segment %q in %q", part, raw)
		}
		if strings.HasPrefix(part, "{") || strings.HasSuffix(part, "}") {
			if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
				return SourceTemplate{}, fmt.Errorf("proxyfs: invalid source template segment %q in %q", part, raw)
			}
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			if !patternNameRE.MatchString(name) {
				return SourceTemplate{}, fmt.Errorf("proxyfs: invalid source template segment %q in %q", part, raw)
			}
			segments = append(segments, sourceSegment{named: true, text: name})
			continue
		}
		if strings.ContainsAny(part, "{}*") {
			return SourceTemplate{}, fmt.Errorf("proxyfs: invalid source template segment %q in %q", part, raw)
		}
		segments = append(segments, sourceSegment{text: part})
	}
	return SourceTemplate{raw: strings.Join(parts, "/"), segments: segments}, nil
}

func defaultSourceTemplate(pattern Pattern) SourceTemplate {
	prefixLen := pattern.prefixLen()
	segments := make([]sourceSegment, 0, prefixLen)
	parts := make([]string, 0, prefixLen)
	for i := 0; i < prefixLen; i++ {
		segment := pattern.segments[i]
		switch segment.kind {
		case patternStatic:
			segments = append(segments, sourceSegment{text: segment.text})
			parts = append(parts, segment.text)
		case patternNamed:
			segments = append(segments, sourceSegment{named: true, text: segment.text})
			parts = append(parts, "{"+segment.text+"}")
		default:
			return SourceTemplate{raw: "."}
		}
	}
	if len(parts) == 0 {
		return SourceTemplate{raw: "."}
	}
	return SourceTemplate{raw: strings.Join(parts, "/"), segments: segments}
}

func (t SourceTemplate) String() string {
	if t.raw == "" {
		return "."
	}
	return t.raw
}

func (t SourceTemplate) Instantiate(params Params) (string, bool) {
	if len(t.segments) == 0 {
		return ".", true
	}
	parts := make([]string, 0, len(t.segments))
	for _, segment := range t.segments {
		if !segment.named {
			parts = append(parts, segment.text)
			continue
		}
		value := params[segment.text]
		if value == "" {
			return "", false
		}
		parts = append(parts, value)
	}
	return joinPath(parts...), true
}

func (t SourceTemplate) MatchSource(sourcePath string) (SourceMatch, bool) {
	sourceParts := splitCleanPath(sourcePath)
	if len(sourceParts) < len(t.segments) {
		return SourceMatch{}, false
	}
	params := make(Params)
	rootParts := make([]string, 0, len(t.segments))
	for i, segment := range t.segments {
		part := sourceParts[i]
		if segment.named {
			params[segment.text] = part
		} else if segment.text != part {
			return SourceMatch{}, false
		}
		rootParts = append(rootParts, part)
	}
	sourceRoot := joinPath(rootParts...)
	sourceRel := joinPath(sourceParts[len(t.segments):]...)
	return SourceMatch{
		Params:     params,
		SourceRoot: sourceRoot,
		SourceRel:  sourceRel,
	}, true
}

type SourceMatch struct {
	Params     Params
	SourceRoot string
	SourceRel  string
}

func matchPatternSegment(segment patternSegment, value string, params Params) bool {
	switch segment.kind {
	case patternStatic:
		return segment.text == value
	case patternNamed:
		params[segment.text] = value
		return true
	case patternWildcard:
		return value != ""
	default:
		return false
	}
}

func splitCleanPath(name string) []string {
	name = cleanRoot(name)
	if name == "." {
		return nil
	}
	return strings.Split(path.Clean(name), "/")
}
