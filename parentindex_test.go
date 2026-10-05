package yaml_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Targeted acceptance command for the work in parentindex.go:
//
//	go test -run 'TestParentIndex|Example_fourNodeParentIndex' -v ./...
//
// or, from another module that depends on gopkg.in/yaml.v3:
//
//	go test -run 'TestParentIndex|Example_fourNodeParentIndex' -v .

// findAnchor walks the content graph only, deduping by node pointer so that
// reference cycles (self-anchored maps) terminate.
func findAnchor(root *yaml.Node, anchor string) *yaml.Node {
	seen := map[*yaml.Node]bool{}
	var walk func(*yaml.Node) *yaml.Node
	walk = func(n *yaml.Node) *yaml.Node {
		if n == nil || seen[n] {
			return nil
		}
		seen[n] = true
		if n.Anchor == anchor {
			return n
		}
		for _, c := range n.Content {
			if found := walk(c); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(root)
}

func aliasNodes(root *yaml.Node, target *yaml.Node) []*yaml.Node {
	seen := map[*yaml.Node]bool{}
	var out []*yaml.Node
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if n.Kind == yaml.AliasNode && n.Alias == target {
			out = append(out, n)
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	return out
}

func collectNodes(root *yaml.Node) []*yaml.Node {
	seen := map[*yaml.Node]bool{}
	var out []*yaml.Node
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
		for _, c := range n.Content {
			walk(c)
		}
		if n.Kind == yaml.AliasNode && n.Alias != nil {
			walk(n.Alias)
		}
	}
	walk(root)
	return out
}

// TestParentIndexSharedAnchor covers the acceptance case: two aliases point
// at the same anchor and therefore give two reference sources; one alias is
// nested inside the anchored mapping itself, so expansion would recurse
// forever while identity-deduped traversal terminates.
func TestParentIndexSharedAnchor(t *testing.T) {
	src := `- &a
  name: me
  b: *a
- *a
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	root := doc.Content[0] // sequence node
	idx := yaml.BuildIndex(root)

	if !idx.Valid() {
		t.Fatal("freshly built index must be valid")
	}

	// Traversal must terminate on the self-reference and index every node
	// identity exactly once: seq, map, two keys, one scalar value and two
	// distinct alias nodes.
	if got := len(collectNodes(root)); got != 7 {
		t.Fatalf("fixture has %d nodes", got)
	}
	if !idx.IsRoot(root) {
		t.Fatal("IsRoot must recognize the root passed to BuildIndex")
	}
	rootID := idx.RootID()
	if !rootID.IsRoot() {
		t.Fatal("RootID must carry the dedicated root identity")
	}

	anchor := findAnchor(root, "a")
	if anchor == nil {
		t.Fatal("anchor a not found")
	}
	if anchor == root {
		t.Fatal("anchor must not be the root")
	}
	anchID, ok := idx.IdentityOf(anchor)
	if !ok {
		t.Fatal("anchored node must be part of the indexed graph")
	}
	if anchID.IsRoot() {
		t.Fatal("anchored node must not carry the root identity")
	}

	aliases := aliasNodes(root, anchor)
	if len(aliases) != 2 {
		t.Fatalf("expected two aliases to the same anchor, got %d", len(aliases))
	}

	// The anchored node has exactly three incoming edges: one content edge
	// (sequence slot 0) plus two alias edges (the two reference sources).
	imp, err := idx.DeleteImpact(anchor)
	if err != nil {
		t.Fatalf("DeleteImpact: %v", err)
	}
	if imp.ContentParents != 1 || imp.AliasRefs != 2 || len(imp.Incoming) != 3 {
		t.Fatalf("unexpected impact: content=%d alias=%d edges=%d",
			imp.ContentParents, imp.AliasRefs, len(imp.Incoming))
	}

	var paths []string
	for _, e := range imp.Incoming {
		paths = append(paths, e.Path.String())
		if e.Kind == yaml.ContentEdge && e.Slot != 0 {
			t.Fatalf("content edge slot = %d, want 0", e.Slot)
		}
	}
	wantPaths := []string{"[0]", "[1]->(*)", "[0]->[3]->(*)"}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("incoming paths = %v, want %v", paths, wantPaths)
	}

	// The self-reference must terminate: identity-deduped traversal reaches
	// the scalar children exactly once instead of expanding forever.
	nameVal := anchor.Content[1] // value of "name": scalar "me"
	edges, err := idx.Incoming(nameVal)
	if err != nil {
		t.Fatalf("Incoming: %v", err)
	}
	if len(edges) != 1 || edges[0].Kind != yaml.ContentEdge || edges[0].Slot != 1 {
		t.Fatalf("scalar incoming = %+v, want one content edge at slot 1", edges)
	}
	if edges[0].Path.String() != "[0]->[1]" {
		t.Fatalf("scalar path = %q, want [0]->[1]", edges[0].Path.String())
	}

	// Each alias node is itself reached through one content edge and has an
	// alias edge pointing at the anchor.
	for _, an := range aliases {
		ae, err := idx.Incoming(an)
		if err != nil {
			t.Fatalf("alias incoming: %v", err)
		}
		if len(ae) != 1 || ae[0].Kind != yaml.ContentEdge {
			t.Fatalf("alias node edges = %+v, want one content edge", ae)
		}
	}

	// Aliases must not have been expanded into copied subtrees: the alias
	// nodes still exist and still point at the shared anchor.
	for _, an := range aliases {
		if an.Kind != yaml.AliasNode || an.Alias != anchor {
			t.Fatal("alias node was mutated by indexing")
		}
	}

	// The graph root keeps its dedicated incoming edge.
	rootEdges, err := idx.Incoming(root)
	if err != nil {
		t.Fatalf("root incoming: %v", err)
	}
	if len(rootEdges) != 1 || rootEdges[0].String() != "<root>" {
		t.Fatalf("root edges = %+v, want the synthetic root edge", rootEdges)
	}
}

// TestParentIndexStaleAfterContentChange covers the acceptance case: after
// Node.Content is changed, old queries must be rejected.
func TestParentIndexStaleAfterContentChange(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("- a\n- b\n"), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	root := doc.Content[0]
	idx := yaml.BuildIndex(root)
	if !idx.Valid() {
		t.Fatal("fresh index must be valid")
	}

	// Mutate the graph: replace a content child.
	root.Content[1] = &yaml.Node{Kind: yaml.ScalarNode, Value: "c"}

	if idx.Valid() {
		t.Fatal("index must detect the Content change and go stale")
	}
	if _, err := idx.Incoming(root); !errors.Is(err, yaml.ErrStaleIndex) {
		t.Fatalf("Incoming after mutation err = %v, want ErrStaleIndex", err)
	}
	if _, err := idx.DeleteImpact(root); !errors.Is(err, yaml.ErrStaleIndex) {
		t.Fatalf("DeleteImpact after mutation err = %v, want ErrStaleIndex", err)
	}
}

// TestParentIndexStaleOnAppendAndAlias checks appends, alias rewiring and
// explicit invalidation.
func TestParentIndexStaleOnAppendAndAlias(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("- &x v\n- *x\n"), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	root := doc.Content[0]
	idx := yaml.BuildIndex(root)

	alias := root.Content[1]
	if alias.Kind != yaml.AliasNode {
		t.Fatal("expected alias at slot 1")
	}

	// Append a new slot: the baseline length diverges.
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "z"})
	if _, err := idx.IncomingID(idx.RootID()); !errors.Is(err, yaml.ErrStaleIndex) {
		t.Fatalf("append must invalidate: %v", err)
	}

	// A rebuilt index tracks the appended graph; rewiring an alias then
	// invalidates it too.
	idx = yaml.BuildIndex(root)
	alias.Alias = &yaml.Node{Kind: yaml.ScalarNode, Value: "elsewhere"}
	if _, err := idx.Incoming(alias); !errors.Is(err, yaml.ErrStaleIndex) {
		t.Fatalf("changing Alias target must invalidate: %v", err)
	}

	// Explicit invalidation right after an edit is also honored.
	var doc2 yaml.Node
	if err := yaml.Unmarshal([]byte("k: v\n"), &doc2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	idx2 := yaml.BuildIndex(doc2.Content[0])
	idx2.Invalidate()
	if idx2.Valid() {
		t.Fatal("Invalidate must mark the index stale")
	}
}

// TestParentIndexMappingSlots checks that mapping keys and values are both
// content slots and that delete impact lists concrete incoming edges.
func TestParentIndexMappingSlots(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("a: 1\nb: 2\n"), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	root := doc.Content[0]
	idx := yaml.BuildIndex(root)

	keyA := root.Content[0]
	val1 := root.Content[1]
	ke, err := idx.Incoming(keyA)
	if err != nil || len(ke) != 1 || ke[0].Slot != 0 {
		t.Fatalf("key edge = %+v err=%v", ke, err)
	}
	ve, err := idx.Incoming(val1)
	if err != nil || len(ve) != 1 || ve[0].Slot != 1 || ve[0].Path.String() != "[1]" {
		t.Fatalf("value edge = %+v err=%v", ve, err)
	}

	imp, err := idx.DeleteImpact(val1)
	if err != nil || imp.ContentParents != 1 || imp.AliasRefs != 0 {
		t.Fatalf("impact = %+v err=%v", imp, err)
	}
	if imp.ID.IsRoot() {
		t.Fatal("mapping value must not carry the root identity")
	}
}

// buildFourNodeExample builds the minimal four-node graph used by the
// example: a root sequence (node 1) containing an alias (node 2) and a
// scalar (node 3); the alias points at the shared scalar value (node 4).
func buildFourNodeExample() (*yaml.Node, *yaml.Node, *yaml.Node, *yaml.Node) {
	shared := &yaml.Node{Kind: yaml.ScalarNode, Value: "v", Anchor: "v"}
	alias := &yaml.Node{Kind: yaml.AliasNode, Value: "v", Alias: shared}
	plain := &yaml.Node{Kind: yaml.ScalarNode, Value: "v"}
	root := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{alias, plain}}
	return root, alias, plain, shared
}

func Example_fourNodeParentIndex() {
	// Four nodes total:
	//   node 1: root sequence [alias, scalar]
	//   node 2: alias *v
	//   node 3: scalar "v" (plain second child)
	//   node 4: scalar "v" with anchor v (shared by node 2)
	root, alias, plain, shared := buildFourNodeExample()
	idx := yaml.BuildIndex(root)

	rootEdges, _ := idx.Incoming(root)
	fmt.Printf("root:    %s\n", rootEdges[0])

	aliasEdges, _ := idx.Incoming(alias)
	fmt.Printf("alias:   %s\n", aliasEdges[0])

	plainEdges, _ := idx.Incoming(plain)
	fmt.Printf("plain:   %s\n", plainEdges[0])

	sharedEdges, _ := idx.Incoming(shared)
	fmt.Printf("shared:  %d incoming edge(s)\n", len(sharedEdges))
	for _, e := range sharedEdges {
		fmt.Printf("shared:  %s\n", e)
	}

	imp, _ := idx.DeleteImpact(shared)
	fmt.Printf("impact:  content parents=%d, alias refs=%d\n",
		imp.ContentParents, imp.AliasRefs)

	// Output:
	// root:    <root>
	// alias:   content edge from [0] (slot 0)
	// plain:   content edge from [1] (slot 1)
	// shared:  1 incoming edge(s)
	// shared:  alias edge from [0]->(*)
	// impact:  content parents=0, alias refs=1
}
