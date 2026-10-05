//
// Copyright (c) 2011-2019 Canonical Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package yaml

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"sync/atomic"
)

// EdgeKind indicates whether a parent reference is a content edge
// (a node held in a parent node Content slice) or an alias edge
// (an alias node pointing at its target through its Alias pointer).
// The two kinds are kept separate so that the impact of deleting a
// node can be assessed precisely: content edges identify the exact
// Content slot to remove, while alias edges identify the alias node
// whose Alias pointer must be cleared.
type EdgeKind int

const (
	// ContentEdge is a reference through a parent node Content slice.
	ContentEdge EdgeKind = iota
	// AliasEdge is a reference through an alias node Alias pointer.
	AliasEdge
)

// ParentEdge describes a single incoming reference to a node. A shared
// node (identified by pointer identity) may appear as the target of any
// number of ParentEdge values, one for every parent position that
// references it.
type ParentEdge struct {
	// Parent is the node that holds the reference. For a content edge
	// the referenced node lives in Parent.Content[Index]; for an alias
	// edge Parent is the alias node and the referenced node is
	// Parent.Alias.
	Parent *Node

	// Kind distinguishes content edges from alias edges.
	Kind EdgeKind

	// Index is the position of the referenced node within
	// Parent.Content for content edges, and -1 for alias edges.
	Index int

	// Path is the reference path from the graph root to Parent
	// (inclusive). The referenced node itself is the target of the
	// query and is not repeated at the end of the path, so the path
	// records where the reference originates.
	Path []*Node
}

// ParentIndex is a read-only index of parent references for a yaml.Node
// tree. It records, for every node in the graph, the actual incoming
// edges (content edges and alias edges kept separate) together with the
// reference path from the root.
//
// The index is bound to a baseline of the graph: it captures a
// structural fingerprint at build time and rejects every query once the
// graph has been modified (Content changed, aliases retargeted, nodes
// added or removed) or the index has been explicitly invalidated. The
// index never modifies the Node tree: it does not touch Node.Content,
// does not rewrite alias nodes, and does not copy aliases into subtrees.
//
// Traversal is deduplicated by node identity (pointer equality), so
// cycles introduced by aliases pointing at ancestors do not cause
// infinite expansion.
type ParentIndex struct {
	root     *Node
	incoming map[*Node][]ParentEdge
	baseline uint64
	stale    atomic.Bool
}

var (
	// ErrIndexStale is returned when the graph has changed since the
	// index was built, or the index was explicitly invalidated. A new
	// index must be built against the current graph.
	ErrIndexStale = errors.New("yaml: parent index is stale: the graph changed since the index was built")

	// ErrNodeNotFound is returned when the queried node is not the root
	// and is not present in the indexed graph.
	ErrNodeNotFound = errors.New("yaml: node is not present in the indexed graph")
)

// BuildParentIndex builds a read-only parent reference index for the
// graph rooted at root. The root is the graph origin and has a special
// identity: queries for it report no incoming edges. The graph is
// traversed in full, deduplicating by node identity so that alias
// cycles are handled without infinite expansion.
func BuildParentIndex(root *Node) *ParentIndex {
	idx := &ParentIndex{
		root:     root,
		incoming: make(map[*Node][]ParentEdge),
	}
	visited := make(map[*Node]bool)
	var walk func(path []*Node, n *Node)
	walk = func(path []*Node, n *Node) {
		if n == nil || visited[n] {
			return
		}
		visited[n] = true
		for i, child := range n.Content {
			if child == nil {
				continue
			}
			idx.incoming[child] = append(idx.incoming[child], ParentEdge{
				Parent: n,
				Kind:   ContentEdge,
				Index:  i,
				Path:   appendPath(path, n),
			})
			walk(appendPath(path, n), child)
		}
		if n.Kind == AliasNode && n.Alias != nil {
			idx.incoming[n.Alias] = append(idx.incoming[n.Alias], ParentEdge{
				Parent: n,
				Kind:   AliasEdge,
				Index:  -1,
				Path:   appendPath(path, n),
			})
			walk(appendPath(path, n), n.Alias)
		}
	}
	walk(nil, root)
	idx.baseline = idx.fingerprint()
	return idx
}

// Root returns the root node the index was built against.
func (idx *ParentIndex) Root() *Node {
	return idx.root
}

// IsRoot reports whether n is the root node of the indexed graph.
func (idx *ParentIndex) IsRoot(n *Node) bool {
	return n == idx.root
}

// Incoming returns the actual incoming edges for n: every parent
// position that references n, with content edges and alias edges kept
// separate and each carrying its reference path from the root. A shared
// node yields one edge per reference site.
//
// The root has a special identity as the graph origin and reports an
// empty (non-nil) edge list. A node not present in the graph yields
// ErrNodeNotFound. If the graph has changed since the index was built
// (or the index was invalidated) ErrIndexStale is returned; build a new
// index against the current graph in that case.
func (idx *ParentIndex) Incoming(n *Node) ([]ParentEdge, error) {
	if idx.stale.Load() {
		return nil, ErrIndexStale
	}
	if idx.fingerprint() != idx.baseline {
		idx.stale.Store(true)
		return nil, ErrIndexStale
	}
	if n == idx.root {
		return []ParentEdge{}, nil
	}
	edges, ok := idx.incoming[n]
	if !ok {
		return nil, ErrNodeNotFound
	}
	out := make([]ParentEdge, len(edges))
	copy(out, edges)
	return out, nil
}

// Invalidate explicitly marks the index as stale. Subsequent queries
// return ErrIndexStale until a new index is built.
func (idx *ParentIndex) Invalidate() {
	idx.stale.Store(true)
}

// Stale reports whether the index is currently known to be stale,
// either because Invalidate was called or because a query detected a
// graph change.
func (idx *ParentIndex) Stale() bool {
	return idx.stale.Load()
}

// Baseline returns the structural fingerprint the index was built
// against. It is exposed for diagnostic purposes; querying the index
// is the supported way to detect staleness.
func (idx *ParentIndex) Baseline() uint64 {
	return idx.baseline
}

// fingerprint computes a structural fingerprint of the current graph.
// Node identity is established by pointer equality and mapped to
// encounter-order ids, so the fingerprint is stable across calls on the
// same graph and changes whenever the graph structure changes.
func (idx *ParentIndex) fingerprint() uint64 {
	h := fnv.New64a()
	ids := make(map[*Node]uint64)
	var nextID uint64
	idOf := func(n *Node) uint64 {
		if v, ok := ids[n]; ok {
			return v
		}
		v := nextID
		nextID++
		ids[n] = v
		return v
	}
	var b [8]byte
	put := func(v uint64) {
		binary.LittleEndian.PutUint64(b[:], v)
		h.Write(b[:])
	}
	putStr := func(s string) {
		put(uint64(len(s)))
		h.Write([]byte(s))
	}
	var visit func(n *Node)
	visit = func(n *Node) {
		if n == nil {
			return
		}
		if _, ok := ids[n]; ok {
			return
		}
		idOf(n)
		put(uint64(n.Kind))
		put(uint64(n.Style))
		put(uint64(n.Line))
		put(uint64(n.Column))
		putStr(n.Tag)
		putStr(n.Value)
		putStr(n.Anchor)
		putStr(n.HeadComment)
		putStr(n.LineComment)
		putStr(n.FootComment)
		put(uint64(len(n.Content)))
		for _, c := range n.Content {
			if c == nil {
				put(^uint64(0))
			} else {
				put(idOf(c))
			}
		}
		if n.Alias == nil {
			put(^uint64(0))
		} else {
			put(idOf(n.Alias))
		}
		for _, c := range n.Content {
			visit(c)
		}
		visit(n.Alias)
	}
	visit(idx.root)
	put(nextID)
	return h.Sum64()
}

// appendPath returns a new slice containing path followed by n, so that
// reference paths recorded in the index are not aliased to the walk
// scratch slices.
func appendPath(path []*Node, n *Node) []*Node {
	out := make([]*Node, len(path)+1)
	copy(out, path)
	out[len(path)] = n
	return out
}
