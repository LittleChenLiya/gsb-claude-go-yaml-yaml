// Copyright 2026 Canonical Ltd.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
// FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
// DEALINGS IN THE SOFTWARE.

package yaml

import (
	"errors"
	"fmt"
	"unsafe"
)

// This file implements a read-only parent-reference index on top of a Node
// tree. The index is a side structure only: it never mutates Node.Content
// and it never expands aliases into copied subtrees. A node that is shared
// (for example referenced by several aliases, or appearing under several
// container slots) keeps one entry per actual incoming edge, and each edge
// records the source path of that particular reference.
//
// Two kinds of incoming edges are tracked separately:
//
//   - ContentEdge: a parent node reaches the target through its Content
//     slice (document/mapping/sequence children, including mapping values).
//   - AliasEdge: an alias node (Kind == AliasNode) reaches the target
//     through its Alias pointer.
//
// Traversal dedupes by node identity, so reference cycles (including a
// self-referential anchor) terminate instead of expanding forever.
//
// The index is bound to the graph baseline captured by BuildIndex. Once the
// graph changes (Node.Content, Node.Alias, or node identity/structure under
// the indexed root), queries against the old index fail with ErrStaleIndex
// rather than reporting silently wrong edges.

// EdgeKind classifies an incoming edge recorded by the index.
type EdgeKind int

const (
	// ContentEdge is an edge from a container node to one of its Content
	// children. Slot is the index into the parent Content slice (keys and
	// values of a mapping are both slots).
	ContentEdge EdgeKind = iota

	// AliasEdge is an edge from an alias node to the node its Alias pointer
	// references. Slot is -1 for alias edges.
	AliasEdge
)

// Step is one hop of a reference path from the graph root to an edge source.
type Step struct {
	// Kind is the kind of edge traversed by this hop.
	Kind EdgeKind
	// Slot is the index inside the parent Content slice for content edges.
	// It is -1 for alias edges.
	Slot int
}

// RefPath is the path of one reference: the steps taken from the graph root
// down to the node that owns an incoming edge. It is nil for the synthetic
// root edge.
type RefPath []Step

// Edge is one actual incoming edge of a node.
type Edge struct {
	// From is the source node of the edge. It is nil for the synthetic edge
	// attached to the graph root itself; inspect FromID.IsRoot in that case.
	From *Node

	// FromID is the identity of the source node. It is never zero and is
	// comparable with ==.
	FromID Identity

	// Kind says whether this is a content edge or an alias edge.
	Kind EdgeKind

	// Slot is the Content slot for content edges, -1 for alias edges.
	Slot int

	// Path is the source path of this particular reference: the steps from
	// the graph root to From. A shared node therefore gets a distinct Path
	// per reference even though the node identity is the same. Path is nil
	// for the synthetic root edge.
	Path RefPath
}

// Identity is the stable identity of a node within an indexed graph. Nodes
// are identified by pointer address, never by value, so two distinct nodes
// with identical content still differ in identity, while aliases referring
// to the same anchored node resolve to the same identity.
//
// Identity is a comparable value type (suitable for map keys) and remains
// valid only as long as the index that produced it is valid.
type Identity struct {
	ptr  uintptr
	root bool
}

// IsRoot reports whether id is the dedicated identity of the graph root
// passed to BuildIndex.
func (id Identity) IsRoot() bool { return id.root }

func nodeID(n *Node) Identity {
	return Identity{ptr: uintptr(unsafe.Pointer(n))}
}

// fingerprint captures the baseline shape of one node without holding any
// aliases into the graph slices: Content is snapshotted pointer-by-pointer,
// so later appends, replacements or re-slicing of Node.Content diverge from
// the baseline.
type fingerprint struct {
	kind    Kind
	value   string
	anchor  string
	alias   Identity // identity of Alias target when kind == AliasNode
	content []Identity
}

type nodeInfo struct {
	node *Node
	// cameFrom is the content edge through which this identity was first
	// reached from the root; nil for the root.
	cameFrom *Edge
}

// ErrStaleIndex is returned by index queries when the indexed Node graph has
// changed since BuildIndex and the index must be rebuilt.
var ErrStaleIndex = errors.New("yaml: parent index is stale because the node graph changed; rebuild it")

// Index is a read-only parent-reference index bound to one graph baseline.
//
// The zero value is not usable; obtain an index with BuildIndex. An Index
// value is a snapshot: it does not observe later graph edits.
type Index struct {
	rootID Identity
	nodes  map[Identity]nodeInfo
	// edges[target] lists one Edge per actual incoming edge. The root has a
	// synthetic root edge so Incoming(root) is non-empty.
	edges      map[Identity][]Edge
	baseline   map[Identity]fingerprint
	baselineOK bool
	stale      bool
}

// BuildIndex builds a read-only parent-reference index for the graph rooted
// at root. The graph is not modified: Content slices are never rewritten and
// aliases are never expanded or copied into subtrees.
//
// Traversal visits each node identity once, so reference cycles terminate;
// alias edges are recorded but not traversed, keeping alias references
// separate from content references. Each recorded edge keeps the source path
// of its own reference, hence a node referenced from several places reports
// one incoming edge per reference.
func BuildIndex(root *Node) *Index {
	idx := &Index{
		rootID:     Identity{ptr: uintptr(unsafe.Pointer(root)), root: true},
		nodes:      make(map[Identity]nodeInfo),
		edges:      make(map[Identity][]Edge),
		baseline:   make(map[Identity]fingerprint),
		baselineOK: true,
	}

	rootEdge := Edge{FromID: idx.rootID, Slot: -1}
	idx.edges[idx.rootID] = []Edge{rootEdge}

	type queueItem struct {
		node     *Node
		id       Identity
		cameFrom *Edge
	}
	queue := []queueItem{{root, idx.rootID, nil}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		n := item.node
		id := item.id

		if _, seen := idx.nodes[id]; seen {
			continue
		}
		idx.nodes[id] = nodeInfo{node: n, cameFrom: item.cameFrom}

		// Snapshot this node baseline.
		fp := fingerprint{
			kind:   n.Kind,
			value:  n.Value,
			anchor: n.Anchor,
		}
		if n.Kind == AliasNode && n.Alias != nil {
			fp.alias = nodeID(n.Alias)
		}
		if len(n.Content) > 0 {
			fp.content = make([]Identity, len(n.Content))
			for i, c := range n.Content {
				fp.content[i] = nodeID(c)
			}
		}
		idx.baseline[id] = fp

		// Content edges: every slot of the Content slice, keys and values.
		for slot, child := range n.Content {
			if child == nil {
				continue
			}
			path := idx.pathTo(id)
			e := Edge{
				From:   n,
				FromID: id,
				Kind:   ContentEdge,
				Slot:   slot,
				Path:   append(path, Step{Kind: ContentEdge, Slot: slot}),
			}
			target := nodeID(child)
			idx.edges[target] = append(idx.edges[target], e)
			if _, seen := idx.nodes[target]; !seen {
				ec := e
				queue = append(queue, queueItem{child, target, &ec})
			}
		}

		// Alias edges are recorded separately and never traversed as
		// subtree expansion: aliases are edges, not copies of the anchored
		// subtree. The anchored node is still registered by identity so its
		// incoming alias edges are queryable even when it is not reachable
		// through content edges; it gets no canonical content path.
		if n.Kind == AliasNode && n.Alias != nil {
			target := nodeID(n.Alias)
			path := idx.pathTo(id)
			e := Edge{
				From:   n,
				FromID: id,
				Kind:   AliasEdge,
				Slot:   -1,
				Path:   append(path, Step{Kind: AliasEdge, Slot: -1}),
			}
			idx.edges[target] = append(idx.edges[target], e)
			if _, seen := idx.nodes[target]; !seen {
				queue = append(queue, queueItem{n.Alias, target, nil})
			}
		}
	}

	return idx
}

// pathTo returns the canonical reference path from the root to id by walking
// the first-reached content edges backwards. The returned slice is freshly
// allocated and owned by the caller.
func (idx *Index) pathTo(id Identity) RefPath {
	var rev []Step
	for {
		info, ok := idx.nodes[id]
		if !ok || info.cameFrom == nil {
			break
		}
		rev = append(rev, Step{Kind: info.cameFrom.Kind, Slot: info.cameFrom.Slot})
		id = info.cameFrom.FromID
	}
	if len(rev) == 0 {
		return nil
	}
	path := make(RefPath, len(rev))
	for i, s := range rev {
		path[len(rev)-1-i] = s
	}
	return path
}

// RootID returns the dedicated identity of the graph root. Identity.IsRoot
// reports true for it.
func (idx *Index) RootID() Identity { return idx.rootID }

// IdentityOf returns the identity of n within this index and whether n is
// part of the indexed graph.
func (idx *Index) IdentityOf(n *Node) (Identity, bool) {
	if n == nil {
		return Identity{}, false
	}
	id := nodeID(n)
	if id.ptr == idx.rootID.ptr {
		id = idx.rootID
	}
	_, ok := idx.nodes[id]
	return id, ok
}

// IsRoot reports whether n is the graph root this index was built for.
func (idx *Index) IsRoot(n *Node) bool {
	return n != nil && nodeID(n).ptr == idx.rootID.ptr
}

// verifyBaseline walks the graph once more, deduping by identity, and
// compares every reached node against the fingerprint captured at build time.
// Any structural change (content slots added, removed, replaced or
// reordered; alias target changed) makes the baseline mismatch.
func (idx *Index) verifyBaseline() bool {
	rootNode := idx.nodes[idx.rootID].node
	if rootNode == nil {
		return false
	}
	seen := make(map[Identity]bool, len(idx.baseline))
	stack := []*Node{rootNode}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil {
			continue
		}
		id := nodeID(n)
		if n == rootNode {
			id = idx.rootID
		}
		if seen[id] {
			continue
		}
		seen[id] = true

		fp, ok := idx.baseline[id]
		if !ok {
			return false
		}
		if n.Kind != fp.kind || n.Value != fp.value || n.Anchor != fp.anchor {
			return false
		}
		var aliasID Identity
		if n.Kind == AliasNode && n.Alias != nil {
			aliasID = nodeID(n.Alias)
			if !seen[aliasID] {
				stack = append(stack, n.Alias)
			}
		}
		if aliasID != fp.alias {
			return false
		}
		if len(n.Content) != len(fp.content) {
			return false
		}
		for i, c := range n.Content {
			if nodeID(c) != fp.content[i] {
				return false
			}
			if c != nil {
				stack = append(stack, c)
			}
		}
	}
	return len(seen) == len(idx.baseline)
}

// Valid reports whether the graph still matches the baseline captured by
// BuildIndex. It never panics; a failed check marks the index stale.
func (idx *Index) Valid() bool {
	if !idx.baselineOK || idx.stale {
		return false
	}
	if !idx.verifyBaseline() {
		idx.stale = true
		return false
	}
	return true
}

// Invalidate explicitly invalidates the index, for example right after the
// caller edits the indexed graph. Subsequent queries return ErrStaleIndex.
func (idx *Index) Invalidate() { idx.stale = true }

// check is invoked before every query.
func (idx *Index) check() error {
	if !idx.baselineOK || idx.stale {
		return ErrStaleIndex
	}
	if !idx.verifyBaseline() {
		idx.stale = true
		return ErrStaleIndex
	}
	return nil
}

// Incoming returns every actual incoming edge of n in reference order:
// the synthetic root edge first for the root, then content edges and alias
// edges in traversal order. Each edge carries the source path of its own
// reference, so a node shared by two aliases yields two alias edges with two
// different paths. It returns ErrStaleIndex if the graph changed.
func (idx *Index) Incoming(n *Node) ([]Edge, error) {
	if err := idx.check(); err != nil {
		return nil, err
	}
	id, ok := idx.IdentityOf(n)
	if !ok {
		return nil, nil
	}
	edges := idx.edges[id]
	out := make([]Edge, len(edges))
	copy(out, edges)
	return out, nil
}

// IncomingID is Incoming addressed by node identity.
func (idx *Index) IncomingID(id Identity) ([]Edge, error) {
	if err := idx.check(); err != nil {
		return nil, err
	}
	edges := idx.edges[id]
	if edges == nil {
		return nil, nil
	}
	out := make([]Edge, len(edges))
	copy(out, edges)
	return out, nil
}

// Impact describes what references a node and would be affected if the node
// were deleted or detached.
type Impact struct {
	// Node is the queried node; nil when queried for the root identity.
	Node *Node
	// ID is the queried identity.
	ID Identity
	// Incoming lists the actual incoming edges that would dangle (the
	// references that must be updated when removing the node). The root
	// carries its dedicated synthetic root edge.
	Incoming []Edge
	// ContentParents counts incoming content edges.
	ContentParents int
	// AliasRefs counts incoming alias edges (aliases pointing at the node).
	AliasRefs int
}

// DeleteImpact reports the actual incoming edges of n, that is the
// references affected by deleting or detaching n. It returns ErrStaleIndex
// if the graph changed since the index was built.
func (idx *Index) DeleteImpact(n *Node) (Impact, error) {
	edges, err := idx.Incoming(n)
	if err != nil {
		return Impact{}, err
	}
	id, _ := idx.IdentityOf(n)
	imp := Impact{Node: n, ID: id, Incoming: edges}
	for _, e := range edges {
		switch e.Kind {
		case ContentEdge:
			imp.ContentParents++
		case AliasEdge:
			imp.AliasRefs++
		}
	}
	return imp, nil
}

// String renders a reference path for diagnostics, e.g. "[0]->[0]" for a
// content hop and "[0]->(*)" for an alias hop.
func (p RefPath) String() string {
	if len(p) == 0 {
		return "(root)"
	}
	s := ""
	for _, st := range p {
		if st.Kind == AliasEdge {
			s += "->(*)"
		} else {
			s += fmt.Sprintf("->[%d]", st.Slot)
		}
	}
	if len(s) >= 2 {
		s = s[2:]
	}
	return s
}

// String renders an edge for diagnostics.
func (e Edge) String() string {
	if e.Slot < 0 && e.Path == nil {
		return "<root>"
	}
	switch e.Kind {
	case AliasEdge:
		return fmt.Sprintf("alias edge from %s", e.Path)
	default:
		return fmt.Sprintf("content edge from %s (slot %d)", e.Path, e.Slot)
	}
}
