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
	"strconv"
)

// ConsumptionKind describes how a YAML mapping key was consumed during
// decoding.
type ConsumptionKind int

const (
	// NotConsumed indicates the key was not consumed by any struct field
	// or custom UnmarshalYAML implementation.
	NotConsumed ConsumptionKind = iota
	// ConsumedByStruct indicates the key was consumed by a struct field.
	ConsumedByStruct
	// ConsumedByUnmarshal indicates the key was consumed by a custom
	// UnmarshalYAML implementation.
	ConsumedByUnmarshal
)

// String returns the name of the consumption kind.
func (k ConsumptionKind) String() string {
	switch k {
	case ConsumedByStruct:
		return "struct"
	case ConsumedByUnmarshal:
		return "unmarshal"
	default:
		return "unknown"
	}
}

// FieldConsumption records how a single YAML mapping key was consumed.
type FieldConsumption struct {
	// Key is the literal key value from the YAML input.
	Key string
	// Node is the literal key node from the YAML input. It holds the
	// real position and literal key value.
	Node *Node
	// Path is the path from the root of the decoded document to this key.
	// Mapping keys are represented by their literal key string; sequence
	// indices are represented by their decimal index.
	Path []string
	// Kind describes how the key was consumed.
	Kind ConsumptionKind
}

// FieldLedger records the consumption of YAML mapping keys during decoding.
//
// A ledger is opt-in: by default decoders do not keep one. Attach a ledger
// to a Decoder with SetLedger, and it will be populated as decoding
// proceeds. Custom UnmarshalYAML implementations may mark the keys they
// consume by calling Node.MarkConsumed.
type FieldLedger struct {
	entries []FieldConsumption
	done    bool
	stopped *Node
	root    *Node
	pathMap map[*Node][]string
}

// Entries returns the field consumptions recorded during decoding.
func (l *FieldLedger) Entries() []FieldConsumption {
	if l == nil {
		return nil
	}
	return l.entries
}

// Done reports whether decoding completed successfully.
func (l *FieldLedger) Done() bool {
	if l == nil {
		return false
	}
	return l.done
}

// StoppedAt returns the node at which decoding stopped, if it failed.
// It returns nil when decoding completed successfully.
func (l *FieldLedger) StoppedAt() *Node {
	if l == nil {
		return nil
	}
	return l.stopped
}

// reset clears the ledger for a fresh decode.
func (l *FieldLedger) reset(root *Node) {
	l.entries = nil
	l.done = false
	l.stopped = nil
	l.root = root
	l.pathMap = nil
}

// mark records the consumption of a key node.
func (l *FieldLedger) mark(n *Node, kind ConsumptionKind) {
	if l == nil || n == nil || l.stopped != nil {
		return
	}
	var path []string
	if l.pathMap != nil {
		path = l.pathMap[n]
	}
	l.entries = append(l.entries, FieldConsumption{
		Key:  n.Value,
		Node: n,
		Path: path,
		Kind: kind,
	})
}

// stop marks the node at which decoding stopped.
func (l *FieldLedger) stop(n *Node) {
	if l == nil || l.stopped != nil {
		return
	}
	l.stopped = n
}

// finish marks decoding as successfully completed.
func (l *FieldLedger) finish() {
	if l == nil {
		return
	}
	l.done = true
}

// buildPathMap walks the decoded node tree and records the path to each
// node by matching node identity. Paths are thus derived from the real
// node tree rather than inferred from the final Go object.
func (l *FieldLedger) buildPathMap() {
	if l == nil || l.root == nil {
		return
	}
	l.pathMap = make(map[*Node][]string)
	root := l.root
	if root.Kind == DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	l.walk(root, nil)
}

func (l *FieldLedger) walk(n *Node, path []string) {
	if n == nil {
		return
	}
	cp := append([]string(nil), path...)
	l.pathMap[n] = cp
	switch n.Kind {
	case MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			val := n.Content[i+1]
			keyPath := append(append([]string(nil), cp...), key.Value)
			l.pathMap[key] = keyPath
			l.walk(val, keyPath)
		}
	case SequenceNode:
		for i, child := range n.Content {
			childPath := append(append([]string(nil), cp...), strconv.Itoa(i))
			l.walk(child, childPath)
		}
	case DocumentNode:
		if len(n.Content) > 0 {
			l.walk(n.Content[0], cp)
		}
	}
}
