package proxyfs

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"sort"
	"time"
)

type Opener interface {
	Open(req OpenRequest) (fs.File, error)
}

type OpenerFunc func(OpenRequest) (fs.File, error)

func (f OpenerFunc) Open(req OpenRequest) (fs.File, error) {
	return f(req)
}

type OpenRequest struct {
	Source fs.FS
	Node   Node
	Route  RouteInfo
	Params Params
}

type nodeInfo struct {
	node Node
	name string
}

func (i nodeInfo) Name() string {
	if i.name != "" {
		return i.name
	}
	if i.node.TargetPath == "." {
		return "."
	}
	return path.Base(i.node.TargetPath)
}

func (i nodeInfo) Size() int64        { return i.node.Size }
func (i nodeInfo) Mode() fs.FileMode  { return i.node.Mode }
func (i nodeInfo) ModTime() time.Time { return i.node.ModTime }
func (i nodeInfo) IsDir() bool        { return i.node.Kind == NodeDir }
func (i nodeInfo) Sys() any           { return nil }

type nodeDirEntry struct {
	node Node
}

func (e nodeDirEntry) Name() string {
	if e.node.TargetPath == "." {
		return "."
	}
	return path.Base(e.node.TargetPath)
}

func (e nodeDirEntry) IsDir() bool                { return e.node.Kind == NodeDir }
func (e nodeDirEntry) Type() fs.FileMode          { return e.node.Mode.Type() }
func (e nodeDirEntry) Info() (fs.FileInfo, error) { return nodeInfo{node: e.node}, nil }

type virtualDirFile struct {
	node    Node
	entries []fs.DirEntry
	offset  int
	closed  bool
}

func newVirtualDirFile(node Node, children []Node) *virtualDirFile {
	entries := make([]fs.DirEntry, 0, len(children))
	for _, child := range children {
		entries = append(entries, nodeDirEntry{node: child})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return &virtualDirFile{node: node, entries: entries}
}

func (f *virtualDirFile) Stat() (fs.FileInfo, error) {
	return nodeInfo{node: f.node}, nil
}

func (f *virtualDirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: f.node.TargetPath, Err: fs.ErrInvalid}
}

func (f *virtualDirFile) Close() error {
	f.closed = true
	return nil
}

func (f *virtualDirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if f.closed {
		return nil, fs.ErrClosed
	}
	if f.offset >= len(f.entries) && n > 0 {
		return nil, io.EOF
	}
	var entries []fs.DirEntry
	if n <= 0 || f.offset+n > len(f.entries) {
		entries = f.entries[f.offset:]
		f.offset = len(f.entries)
	} else {
		entries = f.entries[f.offset : f.offset+n]
		f.offset += n
	}
	return append([]fs.DirEntry(nil), entries...), nil
}

type bytesFile struct {
	reader *bytes.Reader
	info   fs.FileInfo
	closed bool
}

func newBytesFile(name string, data []byte, mode fs.FileMode, modTime time.Time) fs.File {
	if mode == 0 {
		mode = 0444
	}
	return &bytesFile{
		reader: bytes.NewReader(append([]byte(nil), data...)),
		info: staticInfo{
			name:    name,
			size:    int64(len(data)),
			mode:    mode,
			modTime: modTime,
		},
	}
}

func (f *bytesFile) Stat() (fs.FileInfo, error) {
	if f.closed {
		return nil, fs.ErrClosed
	}
	return f.info, nil
}

func (f *bytesFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	return f.reader.Read(p)
}

func (f *bytesFile) Close() error {
	f.closed = true
	return nil
}

type staticInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (i staticInfo) Name() string       { return i.name }
func (i staticInfo) Size() int64        { return i.size }
func (i staticInfo) Mode() fs.FileMode  { return i.mode }
func (i staticInfo) ModTime() time.Time { return i.modTime }
func (i staticInfo) IsDir() bool        { return i.isDir }
func (i staticInfo) Sys() any           { return nil }
