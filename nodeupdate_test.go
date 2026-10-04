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

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseNode unmarshals in into a document node.
func parseNode(t *testing.T, in string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(in), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &n
}

// encodeNode marshals n back to YAML text.
func encodeNode(t *testing.T, n *yaml.Node) string {
	t.Helper()
	out, err := yaml.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

// findScalar walks a mapping path and returns the target scalar node.
func findScalar(t *testing.T, n *yaml.Node, path ...string) *yaml.Node {
	t.Helper()
	cur := n
	if cur.Kind == yaml.DocumentNode {
		cur = cur.Content[0]
	}
	for _, key := range path {
		if cur.Kind != yaml.MappingNode {
			t.Fatalf("path %v: not a mapping at %q", path, key)
		}
		next := (*yaml.Node)(nil)
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == key {
				next = cur.Content[i+1]
				break
			}
		}
		if next == nil {
			t.Fatalf("path %v: key %q not found", path, key)
		}
		cur = next
	}
	if cur.Kind != yaml.ScalarNode {
		t.Fatalf("path %v: target is not a scalar", path)
	}
	return cur
}

// findNode walks a mapping path and returns the target node (scalar or alias).
func findNode(t *testing.T, n *yaml.Node, path ...string) *yaml.Node {
	t.Helper()
	cur := n
	if cur.Kind == yaml.DocumentNode {
		cur = cur.Content[0]
	}
	for _, key := range path {
		if cur.Kind != yaml.MappingNode {
			t.Fatalf("path %v: not a mapping at %q", path, key)
		}
		next := (*yaml.Node)(nil)
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == key {
				next = cur.Content[i+1]
				break
			}
		}
		if next == nil {
			t.Fatalf("path %v: key %q not found", path, key)
		}
		cur = next
	}
	return cur
}

// A documented port scalar, updated in place. The comment, style, and anchor
// identity must survive, and the tag must change from !!int to !!str.
func TestUpdatePathScalar(t *testing.T) {
	doc := parseNode(t, "# the listening port\nport: 80 # inline\n")

	target := findScalar(t, doc, "port")
	if target.Tag != "!!int" {
		t.Fatalf("before: tag = %q, want !!int", target.Tag)
	}

	if err := doc.UpdatePath([]string{"port"}, "auto"); err != nil {
		t.Fatalf("update: %v", err)
	}

	// The node was mutated in place.
	if target.Value != "auto" {
		t.Fatalf("value = %q, want auto", target.Value)
	}
	if target.Tag != "!!str" {
		t.Fatalf("tag = %q, want !!str", target.Tag)
	}
	// Comments and style are preserved.
	if target.LineComment != "# inline" {
		t.Fatalf("line comment = %q", target.LineComment)
	}
	out := encodeNode(t, doc)
	if !strings.Contains(out, "# the listening port") {
		t.Fatalf("head comment lost:\n%s", out)
	}
	if !strings.Contains(out, "# inline") {
		t.Fatalf("line comment lost:\n%s", out)
	}
	if !strings.Contains(out, "port: auto") {
		t.Fatalf("unexpected serialization:\n%s", out)
	}
}

// An alias that references the updated anchor must see the new value, because
// the update mutates the shared node rather than replacing it.
func TestUpdatePathAliasSeesNewValue(t *testing.T) {
	doc := parseNode(t, "server:\n  port: &port 80\n  alias: *port\n")

	anchor := findScalar(t, doc, "server", "port")
	alias := findNode(t, doc, "server", "alias")
	if alias.Kind != yaml.AliasNode || alias.Alias != anchor {
		t.Fatalf("alias does not point at the port anchor")
	}

	if err := doc.UpdatePath([]string{"server", "port"}, "auto"); err != nil {
		t.Fatalf("update: %v", err)
	}

	if anchor.Value != "auto" || anchor.Tag != "!!str" {
		t.Fatalf("anchor not updated: value=%q tag=%q", anchor.Value, anchor.Tag)
	}
	if alias.Alias != anchor {
		t.Fatalf("alias no longer points at the same node")
	}
	if alias.Alias.Value != "auto" {
		t.Fatalf("alias sees value %q, want auto", alias.Alias.Value)
	}
	out := encodeNode(t, doc)
	if !strings.Contains(out, "port: &port auto") {
		t.Fatalf("anchor not re-serialized:\n%s", out)
	}
	if !strings.Contains(out, "alias: *port") {
		t.Fatalf("alias not re-serialized:\n%s", out)
	}
}

// A quoted key is matched by its unquoted value.
func TestUpdatePathQuotedKey(t *testing.T) {
	doc := parseNode(t, "\"port\": 80\n")

	if err := doc.UpdatePath([]string{"port"}, "auto"); err != nil {
		t.Fatalf("update: %v", err)
	}
	out := encodeNode(t, doc)
	if !strings.Contains(out, "\"port\": auto") {
		t.Fatalf("quoted key not preserved:\n%s", out)
	}
}

// The second path failing must leave the first path untouched.
func TestUpdatePathsAtomic(t *testing.T) {
	doc := parseNode(t, "a: 1\nb: 2\n")

	if err := doc.UpdatePaths([]yaml.PathUpdate{
		{Path: []string{"a"}, Value: "100"},
		{Path: []string{"missing"}, Value: "200"},
	}); err == nil {
		t.Fatalf("expected an error for the missing path")
	}

	if a := findScalar(t, doc, "a"); a.Value != "1" {
		t.Fatalf("a was modified to %q; atomicity violated", a.Value)
	}
	if b := findScalar(t, doc, "b"); b.Value != "2" {
		t.Fatalf("b was modified to %q; atomicity violated", b.Value)
	}
}

// A successful batch applies every update.
func TestUpdatePathsBatch(t *testing.T) {
	doc := parseNode(t, "a: 1\nb: 2\nc: 3\n")

	if err := doc.UpdatePaths([]yaml.PathUpdate{
		{Path: []string{"a"}, Value: "100"},
		{Path: []string{"b"}, Value: "200"},
		{Path: []string{"c"}, Value: "300"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if a := findScalar(t, doc, "a"); a.Value != "100" || a.Tag != "!!int" {
		t.Fatalf("a = %q (%s)", a.Value, a.Tag)
	}
	if b := findScalar(t, doc, "b"); b.Value != "200" || b.Tag != "!!int" {
		t.Fatalf("b = %q (%s)", b.Value, b.Tag)
	}
	if c := findScalar(t, doc, "c"); c.Value != "300" || c.Tag != "!!int" {
		t.Fatalf("c = %q (%s)", c.Value, c.Tag)
	}
}

// Duplicate keys are rejected by default.
func TestUpdatePathDuplicateKeysRejected(t *testing.T) {
	doc := parseNode(t, "a: 1\na: 2\n")

	if err := doc.UpdatePath([]string{"a"}, "3"); err == nil {
		t.Fatalf("expected duplicate key error")
	}
	// The tree is untouched.
	if a := findScalar(t, doc, "a"); a.Value != "1" {
		t.Fatalf("tree was modified: a = %q", a.Value)
	}
}

// Duplicate keys can be allowed, in which case the last match wins.
func TestUpdatePathDuplicateKeysAllowed(t *testing.T) {
	doc := parseNode(t, "a: 1\na: 2\n")

	if err := doc.UpdatePath([]string{"a"}, "3", yaml.UpdateOptions{AllowDuplicateKeys: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// findScalar returns the first match; the last match was updated.
	first := findScalar(t, doc, "a")
	if first.Value != "1" {
		t.Fatalf("first duplicate changed: %q", first.Value)
	}
	root := doc.Content[0]
	last := root.Content[len(root.Content)-1]
	if last.Value != "3" {
		t.Fatalf("last duplicate = %q, want 3", last.Value)
	}
}

// Traversing an alias is rejected by default.
func TestUpdatePathAliasTraversalRejected(t *testing.T) {
	doc := parseNode(t, "base: &base\n  port: 80\nserver: *base\n")

	if err := doc.UpdatePath([]string{"server", "port"}, "auto"); err == nil {
		t.Fatalf("expected alias traversal error")
	}
	if port := findScalar(t, doc, "base", "port"); port.Value != "80" {
		t.Fatalf("anchor node was modified: %q", port.Value)
	}
}

// Alias traversal can be opted into.
func TestUpdatePathAliasTraversalAllowed(t *testing.T) {
	doc := parseNode(t, "base: &base\n  port: 80\nserver: *base\n")

	if err := doc.UpdatePath([]string{"server", "port"}, "auto", yaml.UpdateOptions{AllowAliasTraversal: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if port := findScalar(t, doc, "base", "port"); port.Value != "auto" || port.Tag != "!!str" {
		t.Fatalf("anchor not updated: value=%q tag=%q", port.Value, port.Tag)
	}
}

// A missing key leaves the tree untouched.
func TestUpdatePathMissingKey(t *testing.T) {
	doc := parseNode(t, "a: 1\n")

	if err := doc.UpdatePath([]string{"b"}, "2"); err == nil {
		t.Fatalf("expected missing key error")
	}
	if a := findScalar(t, doc, "a"); a.Value != "1" {
		t.Fatalf("tree was modified: a = %q", a.Value)
	}
}

// A path that ends on a mapping (rather than a scalar) is rejected.
func TestUpdatePathTargetNotScalar(t *testing.T) {
	doc := parseNode(t, "a:\n  b: 1\n")

	if err := doc.UpdatePath([]string{"a"}, "2"); err == nil {
		t.Fatalf("expected non-scalar target error")
	}
}

// A path that traverses a scalar (rather than a mapping) is rejected.
func TestUpdatePathSegmentNotMapping(t *testing.T) {
	doc := parseNode(t, "a: 1\n")

	if err := doc.UpdatePath([]string{"a", "b"}, "2"); err == nil {
		t.Fatalf("expected non-mapping segment error")
	}
}

// An empty path is rejected.
func TestUpdatePathEmpty(t *testing.T) {
	doc := parseNode(t, "a: 1\n")

	if err := doc.UpdatePath([]string{}, "2"); err == nil {
		t.Fatalf("expected empty path error")
	}
}

// The tag is re-resolved when the value type changes, and stays correct when
// it does not.
func TestUpdatePathTagTracking(t *testing.T) {
	cases := []struct {
		before string
		value  string
		tag    string
	}{
		{"port: 80\n", "auto", "!!str"},     // int -> string
		{"port: auto\n", "80", "!!int"},     // string -> int
		{"port: \"80\"\n", "auto", "!!str"}, // quoted stays string
		{"port: auto\n", "true", "!!bool"},  // string -> bool
		{"port: true\n", "auto", "!!str"},   // bool -> string
		{"port: auto\n", "~", "!!null"},     // string -> null
	}
	for _, c := range cases {
		doc := parseNode(t, c.before)
		if err := doc.UpdatePath([]string{"port"}, c.value); err != nil {
			t.Fatalf("update %q: %v", c.before, err)
		}
		got := findScalar(t, doc, "port")
		if got.Tag != c.tag {
			t.Errorf("before=%q value=%q: tag = %q, want %q", c.before, c.value, got.Tag, c.tag)
		}
	}
}

// An explicit tag is honoured.
func TestUpdatePathExplicitTag(t *testing.T) {
	doc := parseNode(t, "port: 80\n")

	if err := doc.UpdatePaths([]yaml.PathUpdate{{
		Path:  []string{"port"},
		Value: "80",
		Tag:   "!!str",
	}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := findScalar(t, doc, "port")
	if got.Tag != "!!str" {
		t.Fatalf("tag = %q, want !!str", got.Tag)
	}
	out := encodeNode(t, doc)
	if !strings.Contains(out, "port: \"80\"") {
		t.Fatalf("expected quoted string, got:\n%s", out)
	}
}

// A custom tag is preserved rather than replaced by a resolved built-in tag.
func TestUpdatePathCustomTagPreserved(t *testing.T) {
	doc := parseNode(t, "port: !foo 80\n")

	if err := doc.UpdatePath([]string{"port"}, "auto"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := findScalar(t, doc, "port")
	if got.Tag != "!foo" {
		t.Fatalf("custom tag lost: %q", got.Tag)
	}
	if got.Value != "auto" {
		t.Fatalf("value = %q, want auto", got.Value)
	}
}

// Updating a node that is itself an alias target keeps the alias pointing at
// the same (now updated) node.
func TestUpdatePathAliasTargetIdentity(t *testing.T) {
	doc := parseNode(t, "a: &x 1\nb: *x\n")

	if err := doc.UpdatePath([]string{"a"}, "2"); err != nil {
		t.Fatalf("update: %v", err)
	}
	a := findScalar(t, doc, "a")
	b := findNode(t, doc, "b")
	if b.Kind != yaml.AliasNode || b.Alias != a {
		t.Fatalf("b no longer points at a")
	}
	if a.Value != "2" || b.Alias.Value != "2" {
		t.Fatalf("values not updated: a=%q b=%q", a.Value, b.Alias.Value)
	}
}

// Before/after serialization example.
func ExampleNode_UpdatePath() {
	const src = "# the listening port\nport: 80 # inline\n"

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		panic(err)
	}
	before, err := yaml.Marshal(&doc)
	if err != nil {
		panic(err)
	}

	if err := doc.UpdatePath([]string{"port"}, "auto"); err != nil {
		panic(err)
	}
	after, err := yaml.Marshal(&doc)
	if err != nil {
		panic(err)
	}

	fmt.Println("before:")
	fmt.Print(string(before))
	fmt.Println("after:")
	fmt.Print(string(after))
	// Output:
	// before:
	// # the listening port
	// port: 80 # inline
	// after:
	// # the listening port
	// port: auto # inline
}
