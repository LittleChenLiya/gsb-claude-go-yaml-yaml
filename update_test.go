// Copyright (c) 2026 Canonical Ltd
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

import (
	"errors"
	"fmt"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

const updateFixture = `# server config
server:
  # listen port
  port: &port 80 # primary port
  host: example.com
backup:
  port: *port
`

func decodeDoc(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("cannot decode fixture: %v", err)
	}
	return &doc
}

// mappingValue returns the value node for key in the mapping m.
func mappingValue(t *testing.T, m *yaml.Node, key string) *yaml.Node {
	t.Helper()
	_, v := mappingEntry(t, m, key)
	return v
}

// mappingEntry returns the key and value nodes for key in the mapping m.
func mappingEntry(t *testing.T, m *yaml.Node, key string) (k, v *yaml.Node) {
	t.Helper()
	if m.Kind != yaml.MappingNode {
		t.Fatalf("node at %q is not a mapping (kind %d)", key, m.Kind)
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	t.Fatalf("key %q not found", key)
	return nil, nil
}

func marshalToString(t *testing.T, n *yaml.Node) string {
	t.Helper()
	out, err := yaml.Marshal(n)
	if err != nil {
		t.Fatalf("cannot encode node: %v", err)
	}
	return string(out)
}

// Acceptance: updating the anchored, commented scalar port: 80 to the
// string "auto" keeps the comments, the style, and the anchor identity,
// retags the node as !!str, and the alias *port keeps pointing at the
// very same node and observes the new value.
func TestUpdateScalarsPreservesNodeIdentity(t *testing.T) {
	doc := decodeDoc(t, updateFixture)

	server := mappingValue(t, doc.Content[0], "server")
	portKey, port := mappingEntry(t, server, "port")
	backup := mappingValue(t, doc.Content[0], "backup")
	alias := mappingValue(t, backup, "port")

	if port.Anchor != "port" || port.Value != "80" || port.Tag != "!!int" {
		t.Fatalf("unexpected fixture node: %+v", port)
	}
	if alias.Kind != yaml.AliasNode || alias.Alias != port {
		t.Fatalf("alias does not point at the port node")
	}

	err := yaml.UpdateScalars(doc, yaml.ScalarUpdate{
		Path:  []string{"server", "port"},
		Value: "auto",
	})
	if err != nil {
		t.Fatalf("UpdateScalars failed: %v", err)
	}

	// Value and tag updated in place, on the same node.
	if port.Value != "auto" {
		t.Fatalf("value not updated: %q", port.Value)
	}
	if port.Tag != "!!str" {
		t.Fatalf("tag not retagged to !!str: %q", port.Tag)
	}
	// Comments, style, and anchor identity preserved. The head comment
	// lives on the key node, the line comment on the value node.
	if portKey.HeadComment != "# listen port" {
		t.Fatalf("head comment lost: %q", portKey.HeadComment)
	}
	if port.LineComment != "# primary port" {
		t.Fatalf("line comment lost: %q", port.LineComment)
	}
	if port.Anchor != "port" {
		t.Fatalf("anchor lost: %q", port.Anchor)
	}
	if port.Style != 0 {
		t.Fatalf("style changed: %v", port.Style)
	}
	// The alias still points at the same node and sees the new value.
	if alias.Alias != port {
		t.Fatalf("alias identity changed")
	}
	if alias.Alias.Value != "auto" {
		t.Fatalf("alias sees stale value %q", alias.Alias.Value)
	}

	// Serialized form keeps comments, anchor, and alias.
	got := marshalToString(t, doc)
	want := `# server config
server:
    # listen port
    port: &port auto # primary port
    host: example.com
backup:
    port: *port
`
	if got != want {
		t.Fatalf("unexpected serialization:\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

// Acceptance: when any path in the update list fails, no node in the
// graph is modified, including targets of earlier, valid updates.
func TestUpdateScalarsAtomicOnFailure(t *testing.T) {
	doc := decodeDoc(t, updateFixture)
	before := marshalToString(t, doc)

	server := mappingValue(t, doc.Content[0], "server")
	port := mappingValue(t, server, "port")

	err := yaml.UpdateScalars(doc,
		yaml.ScalarUpdate{Path: []string{"server", "port"}, Value: "auto"},
		yaml.ScalarUpdate{Path: []string{"server", "missing"}, Value: 1},
	)
	if !errors.Is(err, yaml.ErrUpdatePathNotFound) {
		t.Fatalf("expected ErrUpdatePathNotFound, got %v", err)
	}
	if port.Value != "80" || port.Tag != "!!int" {
		t.Fatalf("first update leaked through: value=%q tag=%q", port.Value, port.Tag)
	}
	if after := marshalToString(t, doc); after != before {
		t.Fatalf("graph modified despite failure:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
}

// Quoted keys are matched by their decoded key-node value, not by the
// raw source text.
func TestUpdateScalarsQuotedKeys(t *testing.T) {
	doc := decodeDoc(t, `"a b": 1
"80": old
nested:
  "x\ty": 2
`)

	err := yaml.UpdateScalars(doc,
		yaml.ScalarUpdate{Path: []string{"a b"}, Value: "one"},
		yaml.ScalarUpdate{Path: []string{"80"}, Value: "new"},
		yaml.ScalarUpdate{Path: []string{"nested", "x\ty"}, Value: 3},
	)
	if err != nil {
		t.Fatalf("UpdateScalars failed: %v", err)
	}
	root := doc.Content[0]
	if got := mappingValue(t, root, "a b"); got.Value != "one" || got.Tag != "!!str" {
		t.Fatalf(`"a b" not updated: %+v`, got)
	}
	if got := mappingValue(t, root, "80"); got.Value != "new" || got.Tag != "!!str" {
		t.Fatalf(`"80" not updated: %+v`, got)
	}
	nested := mappingValue(t, root, "nested")
	if got := mappingValue(t, nested, "x\ty"); got.Value != "3" || got.Tag != "!!int" {
		t.Fatalf(`"x\ty" not updated: %+v`, got)
	}
}

// A mapping containing duplicate keys that both match a path segment is
// rejected explicitly instead of silently picking one.
func TestUpdateScalarsDuplicateKeyRejected(t *testing.T) {
	doc := decodeDoc(t, "a: 1\na: 2\nb: 3\n")
	before := marshalToString(t, doc)

	err := yaml.UpdateScalars(doc, yaml.ScalarUpdate{Path: []string{"a"}, Value: 9})
	if !errors.Is(err, yaml.ErrUpdateDuplicateKey) {
		t.Fatalf("expected ErrUpdateDuplicateKey, got %v", err)
	}
	if after := marshalToString(t, doc); after != before {
		t.Fatalf("graph modified despite duplicate key rejection")
	}
}

// Paths that would traverse an alias, or terminate at one, are rejected
// explicitly. The anchored node itself remains updatable via its real
// path.
func TestUpdateScalarsAliasTraversalRejected(t *testing.T) {
	doc := decodeDoc(t, `defaults: &d
  port: 80
prod: *d
`)
	before := marshalToString(t, doc)

	// Traversing through the alias.
	err := yaml.UpdateScalars(doc, yaml.ScalarUpdate{Path: []string{"prod", "port"}, Value: 81})
	if !errors.Is(err, yaml.ErrUpdateAliasTraversal) {
		t.Fatalf("expected ErrUpdateAliasTraversal, got %v", err)
	}
	// Terminating at the alias.
	err = yaml.UpdateScalars(doc, yaml.ScalarUpdate{Path: []string{"prod"}, Value: "x"})
	if !errors.Is(err, yaml.ErrUpdateAliasTraversal) {
		t.Fatalf("expected ErrUpdateAliasTraversal, got %v", err)
	}
	if after := marshalToString(t, doc); after != before {
		t.Fatalf("graph modified despite alias rejection")
	}

	// The real path to the anchored mapping still works, and the alias
	// observes the change through the shared node.
	err = yaml.UpdateScalars(doc, yaml.ScalarUpdate{Path: []string{"defaults", "port"}, Value: 81})
	if err != nil {
		t.Fatalf("UpdateScalars via real path failed: %v", err)
	}
	defaults := mappingValue(t, doc.Content[0], "defaults")
	port := mappingValue(t, defaults, "port")
	prod := mappingValue(t, doc.Content[0], "prod")
	if prod.Alias != defaults {
		t.Fatalf("alias no longer points at the defaults mapping")
	}
	if got := mappingValue(t, prod.Alias, "port"); got != port || got.Value != "81" {
		t.Fatalf("alias does not observe the update: %+v", got)
	}
}

// Explicit rejection policies for the remaining failure modes.
func TestUpdateScalarsRejections(t *testing.T) {
	src := `root:
  scalar: 1
  seq:
    - 1
    - 2
  map:
    k: v
`
	cases := []struct {
		name    string
		updates []yaml.ScalarUpdate
		wantErr error
	}{
		{"empty path", []yaml.ScalarUpdate{{Path: nil, Value: 1}}, yaml.ErrUpdateEmptyPath},
		{"missing key", []yaml.ScalarUpdate{{Path: []string{"root", "nope"}, Value: 1}}, yaml.ErrUpdatePathNotFound},
		{"descend into scalar", []yaml.ScalarUpdate{{Path: []string{"root", "scalar", "x"}, Value: 1}}, yaml.ErrUpdateNotMapping},
		{"descend into sequence", []yaml.ScalarUpdate{{Path: []string{"root", "seq", "0"}, Value: 1}}, yaml.ErrUpdateNotMapping},
		{"target is mapping", []yaml.ScalarUpdate{{Path: []string{"root", "map"}, Value: 1}}, yaml.ErrUpdateNotScalar},
		{"target is sequence", []yaml.ScalarUpdate{{Path: []string{"root", "seq"}, Value: 1}}, yaml.ErrUpdateNotScalar},
		{"unsupported value", []yaml.ScalarUpdate{{Path: []string{"root", "scalar"}, Value: struct{}{}}}, yaml.ErrUpdateUnsupportedValue},
		{"conflicting updates", []yaml.ScalarUpdate{
			{Path: []string{"root", "scalar"}, Value: 1},
			{Path: []string{"root", "scalar"}, Value: 2},
		}, yaml.ErrUpdateConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := decodeDoc(t, src)
			before := marshalToString(t, doc)
			err := yaml.UpdateScalars(doc, c.updates...)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("expected %v, got %v", c.wantErr, err)
			}
			if after := marshalToString(t, doc); after != before {
				t.Fatalf("graph modified despite rejection %v", c.wantErr)
			}
		})
	}
}

// Changing the value type retags the node; the serialized output uses
// the representation of the new type.
func TestUpdateScalarsTypeChanges(t *testing.T) {
	cases := []struct {
		value   interface{}
		wantTag string
		wantOut string
	}{
		{"auto", "!!str", "k: auto\n"},
		{"80", "!!str", `k: "80"` + "\n"}, // string that looks like an int stays a string
		{8080, "!!int", "k: 8080\n"},
		{uint64(1 << 60), "!!int", "k: 1152921504606846976\n"},
		{true, "!!bool", "k: true\n"},
		{nil, "!!null", "k: null\n"},
		{1.5, "!!float", "k: 1.5\n"},
		{float32(2.5), "!!float", "k: 2.5\n"},
	}
	for _, c := range cases {
		doc := decodeDoc(t, "k: old\n")
		err := yaml.UpdateScalars(doc, yaml.ScalarUpdate{Path: []string{"k"}, Value: c.value})
		if err != nil {
			t.Fatalf("UpdateScalars(%v) failed: %v", c.value, err)
		}
		node := mappingValue(t, doc.Content[0], "k")
		if node.Tag != c.wantTag {
			t.Fatalf("UpdateScalars(%v): tag = %q, want %q", c.value, node.Tag, c.wantTag)
		}
		if got := marshalToString(t, doc); got != c.wantOut {
			t.Fatalf("UpdateScalars(%v): output = %q, want %q", c.value, got, c.wantOut)
		}
	}
}

// ExampleUpdateScalars updates a commented, anchored scalar in place and
// shows the node tree before and after serialization.
func ExampleUpdateScalars() {
	src := `# server config
server:
  # listen port
  port: &port 80 # primary port
backup:
  port: *port
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		panic(err)
	}
	before, _ := yaml.Marshal(&doc)
	fmt.Printf("before:\n%s", before)

	err := yaml.UpdateScalars(&doc, yaml.ScalarUpdate{
		Path:  []string{"server", "port"},
		Value: "auto",
	})
	if err != nil {
		panic(err)
	}
	after, _ := yaml.Marshal(&doc)
	fmt.Printf("after:\n%s", after)

	// Output:
	// before:
	// # server config
	// server:
	//     # listen port
	//     port: &port 80 # primary port
	// backup:
	//     port: *port
	// after:
	// # server config
	// server:
	//     # listen port
	//     port: &port auto # primary port
	// backup:
	//     port: *port
}
