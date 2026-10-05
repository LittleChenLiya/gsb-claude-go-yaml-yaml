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

package yaml_test

// Targeted go test commands for the parent index:
//
//   go test -run TestParentIndexFourNodes ./...
//   go test -run TestParentIndexTwoAliasesOneAnchor ./...
//   go test -run TestParentIndexSelfReference ./...
//   go test -run TestParentIndexStaleAfterContentChange ./...
//   go test -run TestParentIndexRootIdentity ./...
//   go test -run TestParentIndex ./...
//
// All of them are small, deterministic, and run against the local
// worktree only.

import (
	"errors"
	"testing"

	"gopkg.in/yaml.v3"
)

// edgeKey identifies a single incoming edge for set comparison.
type edgeKey struct {
	parent *yaml.Node
	kind   yaml.EdgeKind
	index  int
}

func edgeKeys(edges []yaml.ParentEdge) map[edgeKey]bool {
	out := make(map[edgeKey]bool, len(edges))
	for _, e := range edges {
		out[edgeKey{parent: e.Parent, kind: e.Kind, index: e.Index}] = true
	}
	return out
}

func assertEdge(t *testing.T, edges []yaml.ParentEdge, parent *yaml.Node, kind yaml.EdgeKind, index int) {
	t.Helper()
	for _, e := range edges {
		if e.Parent == parent && e.Kind == kind && e.Index == index {
			return
		}
	}
	t.Fatalf("expected edge parent=%p kind=%d index=%d not found in %v", parent, kind, index, edgeKeys(edges))
}

func assertPath(t *testing.T, edges []yaml.ParentEdge, parent *yaml.Node, want []*yaml.Node) {
	t.Helper()
	for _, e := range edges {
		if e.Parent != parent {
			continue
		}
		if len(e.Path) != len(want) {
			t.Fatalf("path length for parent=%p: got %d want %d (%v)", parent, len(e.Path), len(want), e.Path)
		}
		for i := range want {
			if e.Path[i] != want[i] {
				t.Fatalf("path mismatch at %d for parent=%p: got %v want %v", i, parent, e.Path, want)
			}
		}
		return
	}
	t.Fatalf("no edge for parent=%p to check path", parent)
}

// TestParentIndexFourNodes is the four-node example: a document root,
// a sequence, an anchored scalar, and an alias to that anchor. The
// anchor is referenced twice: once as a content child of the sequence
// and once through the alias, so it has two incoming edges of different
// kinds. The alias node itself has one content edge, the sequence has
// one content edge from the root, and the root has the special empty
// result.
func TestParentIndexFourNodes(t *testing.T) {
	root := &yaml.Node{Kind: yaml.DocumentNode}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	anchor := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "1", Anchor: "x"}
	alias := &yaml.Node{Kind: yaml.AliasNode, Alias: anchor}
	root.Content = []*yaml.Node{seq}
	seq.Content = []*yaml.Node{anchor, alias}

	idx := yaml.BuildParentIndex(root)
	if idx.Root() != root {
		t.Fatal("Root() mismatch")
	}
	if !idx.IsRoot(root) || idx.IsRoot(seq) {
		t.Fatal("IsRoot mismatch")
	}

	// Root has the special empty result.
	edges, err := idx.Incoming(root)
	if err != nil {
		t.Fatalf("Incoming(root): %v", err)
	}
	if edges == nil || len(edges) != 0 {
		t.Fatalf("root should have empty non-nil edge list, got %v", edges)
	}

	// Sequence: one content edge from root at index 0.
	edges, err = idx.Incoming(seq)
	if err != nil {
		t.Fatalf("Incoming(seq): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("seq edges: got %d want 1 (%v)", len(edges), edges)
	}
	assertEdge(t, edges, root, yaml.ContentEdge, 0)
	assertPath(t, edges, root, []*yaml.Node{root})

	// Anchor: two incoming edges — content from seq[0] and alias from
	// the alias node.
	edges, err = idx.Incoming(anchor)
	if err != nil {
		t.Fatalf("Incoming(anchor): %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("anchor edges: got %d want 2 (%v)", len(edges), edges)
	}
	assertEdge(t, edges, seq, yaml.ContentEdge, 0)
	assertEdge(t, edges, alias, yaml.AliasEdge, -1)
	assertPath(t, edges, seq, []*yaml.Node{root, seq})
	assertPath(t, edges, alias, []*yaml.Node{root, seq, alias})

	// Alias node: one content edge from seq[1].
	edges, err = idx.Incoming(alias)
	if err != nil {
		t.Fatalf("Incoming(alias): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("alias edges: got %d want 1 (%v)", len(edges), edges)
	}
	assertEdge(t, edges, seq, yaml.ContentEdge, 1)
	assertPath(t, edges, seq, []*yaml.Node{root, seq})

	// A node outside the graph is not found.
	outsider := &yaml.Node{Kind: yaml.ScalarNode, Value: "nope"}
	_, err = idx.Incoming(outsider)
	if !errors.Is(err, yaml.ErrNodeNotFound) {
		t.Fatalf("outsider: got %v want ErrNodeNotFound", err)
	}
}

// TestParentIndexTwoAliasesOneAnchor parses a document where one anchor
// is referenced by two aliases and also held directly as a content
// child. The anchor must report three incoming edges: one content edge
// and two alias edges, one per reference source.
func TestParentIndexTwoAliasesOneAnchor(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("a: &x 1\nb: *x\nc: *x\n"), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	idx := yaml.BuildParentIndex(&root)

	// Locate the anchor node: the value of the first mapping pair.
	mapping := root.Content[0]
	anchor := mapping.Content[1]
	if anchor.Anchor != "x" {
		t.Fatalf("expected anchor x, got %q", anchor.Anchor)
	}

	edges, err := idx.Incoming(anchor)
	if err != nil {
		t.Fatalf("Incoming(anchor): %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("anchor edges: got %d want 3 (%v)", len(edges), edges)
	}
	// One content edge from the mapping at index 1.
	assertEdge(t, edges, mapping, yaml.ContentEdge, 1)
	// Two alias edges, one from each alias node.
	aliasCount := 0
	for _, e := range edges {
		if e.Kind == yaml.AliasEdge {
			aliasCount++
			if e.Parent.Kind != yaml.AliasNode || e.Parent.Alias != anchor {
				t.Fatalf("alias edge does not point at anchor: %v", e)
			}
		}
	}
	if aliasCount != 2 {
		t.Fatalf("alias edges: got %d want 2", aliasCount)
	}
	// The two alias edges have distinct reference sources (paths).
	paths := map[*yaml.Node]bool{}
	for _, e := range edges {
		if e.Kind != yaml.AliasEdge {
			continue
		}
		paths[e.Parent] = true
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 distinct alias sources, got %d", len(paths))
	}
	// Each alias edge path must end at its own alias node and start at
	// the root.
	for _, e := range edges {
		if e.Kind != yaml.AliasEdge {
			continue
		}
		if len(e.Path) < 2 || e.Path[0] != &root || e.Path[len(e.Path)-1] != e.Parent {
			t.Fatalf("alias path malformed: %v", e.Path)
		}
	}
}

// TestParentIndexSelfReference parses an anchor on a sequence that
// contains an alias to itself. Traversal must deduplicate by node
// identity and not expand the cycle infinitely. The sequence reports a
// content edge from the root and an alias edge from the alias; the
// alias node reports a content edge from the sequence.
func TestParentIndexSelfReference(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("&x [*x]\n"), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	idx := yaml.BuildParentIndex(&root)

	// Locate the anchored sequence: root.Content[0].
	seq := root.Content[0]
	if seq.Anchor != "x" {
		t.Fatalf("expected anchor x, got %q", seq.Anchor)
	}
	alias := seq.Content[0]
	if alias.Kind != yaml.AliasNode || alias.Alias != seq {
		t.Fatalf("expected alias to seq, got kind=%v", alias.Kind)
	}

	// Sequence: content edge from root + alias edge from the alias.
	edges, err := idx.Incoming(seq)
	if err != nil {
		t.Fatalf("Incoming(seq): %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("seq edges: got %d want 2 (%v)", len(edges), edges)
	}
	assertEdge(t, edges, &root, yaml.ContentEdge, 0)
	assertEdge(t, edges, alias, yaml.AliasEdge, -1)

	// Alias node: content edge from seq at index 0.
	edges, err = idx.Incoming(alias)
	if err != nil {
		t.Fatalf("Incoming(alias): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("alias edges: got %d want 1 (%v)", len(edges), edges)
	}
	assertEdge(t, edges, seq, yaml.ContentEdge, 0)

	// Root still has the special empty result.
	edges, err = idx.Incoming(&root)
	if err != nil || len(edges) != 0 {
		t.Fatalf("root: got %v err %v", edges, err)
	}
}

// TestParentIndexStaleAfterContentChange builds an index, confirms a
// query works, then mutates Node.Content and confirms the old index
// rejects the query with ErrIndexStale. Explicit invalidation is also
// covered.
func TestParentIndexStaleAfterContentChange(t *testing.T) {
	root := &yaml.Node{Kind: yaml.DocumentNode}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = []*yaml.Node{seq}

	idx := yaml.BuildParentIndex(root)
	if idx.Stale() {
		t.Fatal("fresh index should not be stale")
	}
	if _, err := idx.Incoming(seq); err != nil {
		t.Fatalf("Incoming(seq) before change: %v", err)
	}

	// Mutate Content: the old index must reject subsequent queries.
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "new"})
	if _, err := idx.Incoming(seq); !errors.Is(err, yaml.ErrIndexStale) {
		t.Fatalf("Incoming(seq) after Content change: got %v want ErrIndexStale", err)
	}
	if !idx.Stale() {
		t.Fatal("index should be marked stale after rejected query")
	}

	// A freshly built index against the changed graph works.
	idx2 := yaml.BuildParentIndex(root)
	if _, err := idx2.Incoming(seq); err != nil {
		t.Fatalf("Incoming(seq) on rebuilt index: %v", err)
	}

	// Explicit invalidation also rejects queries.
	idx2.Invalidate()
	if !idx2.Stale() {
		t.Fatal("index should be stale after Invalidate")
	}
	if _, err := idx2.Incoming(seq); !errors.Is(err, yaml.ErrIndexStale) {
		t.Fatalf("Incoming(seq) after Invalidate: got %v want ErrIndexStale", err)
	}
}

// TestParentIndexRootIdentity pins down the special root identity: the
// root is recognized (not ErrNodeNotFound), reports an empty non-nil
// edge list, and IsRoot distinguishes it from other nodes.
func TestParentIndexRootIdentity(t *testing.T) {
	root := &yaml.Node{Kind: yaml.DocumentNode}
	child := &yaml.Node{Kind: yaml.ScalarNode, Value: "x"}
	root.Content = []*yaml.Node{child}
	idx := yaml.BuildParentIndex(root)

	if !idx.IsRoot(root) {
		t.Fatal("IsRoot(root) should be true")
	}
	if idx.IsRoot(child) {
		t.Fatal("IsRoot(child) should be false")
	}
	if idx.Root() != root {
		t.Fatal("Root() mismatch")
	}
	edges, err := idx.Incoming(root)
	if err != nil {
		t.Fatalf("Incoming(root): %v", err)
	}
	if edges == nil || len(edges) != 0 {
		t.Fatalf("root should have empty non-nil edge list, got %v", edges)
	}

	// A nil-rooted index treats nil as the root.
	nilIdx := yaml.BuildParentIndex(nil)
	if !nilIdx.IsRoot(nil) {
		t.Fatal("nil root should be recognized as root")
	}
	edges, err = nilIdx.Incoming(nil)
	if err != nil || len(edges) != 0 {
		t.Fatalf("nil root: got %v err %v", edges, err)
	}
}
