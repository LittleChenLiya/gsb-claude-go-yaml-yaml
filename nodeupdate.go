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
	"fmt"
)

// PathUpdate describes a single scalar value replacement at a mapping path.
//
// The path is matched against the real key nodes of the document: each
// element is compared to the Value of the corresponding key node, so quoted
// keys (for example "port") match their unquoted form. The target node is
// updated in place, which means its comments, style, and anchor identity
// are preserved, and every alias that references the updated anchor sees the
// new value.
type PathUpdate struct {
	// Path is the sequence of mapping keys that locate the target scalar.
	Path []string

	// Value is the new raw scalar content (unquoted and unescaped).
	Value string

	// Tag optionally specifies the YAML tag for the new value (for example
	// "!!str"). When empty the tag is resolved from Value, and the target
	// node's tag is updated to match the new value's type so that a former
	// !!int node does not keep pretending to be an integer.
	Tag string
}

// UpdateOptions controls how a path update is resolved.
//
// The zero value rejects alias traversal and duplicate keys, which keeps an
// update unambiguous and prevents accidentally modifying a node that is
// shared with other parts of the document through an alias.
type UpdateOptions struct {
	// AllowAliasTraversal permits a path to cross an alias node. By default
	// this is rejected because an alias shares its target node with other
	// parts of the document, so updating it would silently change every
	// reference.
	AllowAliasTraversal bool

	// AllowDuplicateKeys permits an update when a mapping contains more than
	// one key with the same value. By default this is rejected because the
	// target is ambiguous. When allowed, the last matching key is updated,
	// matching the behaviour of the decoder.
	AllowDuplicateKeys bool
}

// UpdatePath replaces the scalar value at the given mapping path, preserving
// the target node's comments, style, and anchor identity. It is a convenience
// wrapper around UpdatePaths for a single update.
//
// See UpdatePaths for the supported range and the semantics of opts.
func (n *Node) UpdatePath(path []string, value string, opts ...UpdateOptions) error {
	return n.UpdatePaths([]PathUpdate{{Path: path, Value: value}}, opts...)
}

// UpdatePaths replaces the scalar values at the given mapping paths.
//
// Every path is resolved and validated before any node is modified, so a
// failure on one path leaves the original node graph untouched. This makes
// the update atomic: either all updates are applied or none are.
//
// The target node is updated in place. Its comments, style, and anchor
// identity are preserved, and any alias that references the updated anchor
// sees the new value. The node's tag is updated to match the new value's
// type (unless an explicit tag is provided), so a former !!int node changed
// to a string no longer keeps an obsolete !!int tag.
//
// By default, traversing an alias or finding duplicate keys in a mapping is
// rejected. Pass an UpdateOptions value to opt in.
//
// Only scalar values at mapping paths are supported: mapping and sequence
// values cannot be replaced with a scalar, keys cannot be renamed, and
// sequence indexes are not traversed.
func (n *Node) UpdatePaths(updates []PathUpdate, opts ...UpdateOptions) error {
	var o UpdateOptions
	if len(opts) > 0 {
		o = opts[0]
	}

	// Resolve and validate every path before touching any node. This keeps
	// the update atomic: a failure on one path leaves the tree untouched.
	type resolved struct {
		node *Node
		tag  string
	}
	targets := make([]resolved, len(updates))
	for i, u := range updates {
		target, err := n.resolvePath(u.Path, o)
		if err != nil {
			return err
		}
		tag := u.Tag
		if tag == "" {
			tag = resolveScalarTag(target.Style, target.Tag, u.Value)
		}
		targets[i] = resolved{node: target, tag: tag}
	}

	// All paths are valid; apply the updates in place.
	for i, u := range updates {
		t := targets[i]
		t.node.Value = u.Value
		t.node.Tag = t.tag
	}
	return nil
}

// resolveScalarTag computes the tag that a scalar node should carry after its
// value is replaced with value. A quoted scalar is always a string, a custom
// (non-resolvable) tag is preserved, and a plain scalar's tag is resolved
// from its content.
func resolveScalarTag(style Style, oldTag, value string) string {
	if style&(SingleQuotedStyle|DoubleQuotedStyle|LiteralStyle|FoldedStyle) != 0 {
		return strTag
	}
	if oldTag != "" && !resolvableTag(oldTag) {
		return oldTag
	}
	tag, _ := resolve("", value)
	return tag
}

// resolvePath walks the path from n and returns the target scalar node.
func (n *Node) resolvePath(path []string, opts UpdateOptions) (*Node, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("yaml: cannot update an empty path")
	}
	cur := n
	for i, key := range path {
		// A document node wraps the actual root.
		if cur.Kind == DocumentNode {
			if len(cur.Content) != 1 {
				return nil, fmt.Errorf("yaml: path %v: document has %d root nodes", path, len(cur.Content))
			}
			cur = cur.Content[0]
		}
		if cur.Kind == AliasNode {
			if !opts.AllowAliasTraversal {
				return nil, fmt.Errorf("yaml: path %v: segment %d traverses an alias", path, i)
			}
			if cur.Alias == nil {
				return nil, fmt.Errorf("yaml: path %v: alias at segment %d has no target", path, i)
			}
			cur = cur.Alias
		}
		if cur.Kind != MappingNode {
			return nil, fmt.Errorf("yaml: path %v: segment %d is not a mapping", path, i)
		}

		var found *Node
		for j := 0; j+1 < len(cur.Content); j += 2 {
			k := cur.Content[j]
			if k.Kind == AliasNode {
				if !opts.AllowAliasTraversal {
					return nil, fmt.Errorf("yaml: path %v: key at segment %d is an alias", path, i)
				}
				if k.Alias == nil {
					return nil, fmt.Errorf("yaml: path %v: alias key at segment %d has no target", path, i)
				}
				k = k.Alias
			}
			if k.Kind == ScalarNode && k.Value == key {
				if found != nil && !opts.AllowDuplicateKeys {
					return nil, fmt.Errorf("yaml: path %v: duplicate key %q", path, key)
				}
				found = cur.Content[j+1]
			}
		}
		if found == nil {
			return nil, fmt.Errorf("yaml: path %v: key %q not found", path, key)
		}
		cur = found
	}
	if cur.Kind == AliasNode {
		if !opts.AllowAliasTraversal {
			return nil, fmt.Errorf("yaml: path %v: target is an alias", path)
		}
		if cur.Alias == nil {
			return nil, fmt.Errorf("yaml: path %v: target alias has no target", path)
		}
		cur = cur.Alias
	}
	if cur.Kind != ScalarNode {
		return nil, fmt.Errorf("yaml: path %v: target is not a scalar", path)
	}
	return cur, nil
}
