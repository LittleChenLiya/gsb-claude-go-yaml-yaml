package yaml

import (
	"errors"
	"fmt"
	"strconv"
)

// Explicit rejection policies for UpdateScalars. Callers may test the
// returned error with errors.Is against these sentinels.
var (
	// ErrUpdateEmptyPath rejects an update whose path has no segments.
	ErrUpdateEmptyPath = errors.New("yaml: update path is empty")
	// ErrUpdatePathNotFound rejects a path segment that matches no key.
	ErrUpdatePathNotFound = errors.New("yaml: update path not found")
	// ErrUpdateDuplicateKey rejects a path segment matching more than one
	// key in the same mapping (duplicate keys are ambiguous).
	ErrUpdateDuplicateKey = errors.New("yaml: update path matches duplicate keys")
	// ErrUpdateAliasTraversal rejects any path that would traverse or
	// terminate at an alias node. Aliases are never followed, so the
	// anchored node they point to is updated only via its real path.
	ErrUpdateAliasTraversal = errors.New("yaml: update path traverses an alias node")
	// ErrUpdateNotMapping rejects a path segment resolved below a node
	// that is not a mapping (only mapping paths are supported).
	ErrUpdateNotMapping = errors.New("yaml: update path segment is not a mapping")
	// ErrUpdateNotScalar rejects a path whose final node is not a scalar.
	ErrUpdateNotScalar = errors.New("yaml: update target is not a scalar node")
	// ErrUpdateConflict rejects an update list in which two entries
	// resolve to the same node.
	ErrUpdateConflict = errors.New("yaml: multiple updates target the same node")
	// ErrUpdateUnsupportedValue rejects a Go value that is not a
	// supported scalar type.
	ErrUpdateUnsupportedValue = errors.New("yaml: unsupported scalar value type")
)

// ScalarUpdate describes a single in-place replacement of the scalar
// value sitting at a mapping path within a decoded node tree.
//
// Path is a sequence of mapping keys matched against the real key nodes
// of each mapping, from the document root down. Keys are compared by
// their decoded Value, so source-level quoting is irrelevant: the path
// segment "a b" matches the key node of `"a b": 1`, and "80" matches
// both `80: 1` and `"80": 1`. Merge keys (<<) are never matched.
//
// Value must be one of the supported scalar types: string, bool, nil,
// any sized int or uint, float32, or float64. The target node's Tag is
// recomputed from the Go type of Value, so a node never keeps a tag
// pretending to be the old type (e.g. replacing 80 with "auto" retags
// the node from !!int to !!str).
type ScalarUpdate struct {
	Path  []string
	Value interface{}
}

// UpdateScalars rewrites the scalar values at the given mapping paths of
// the node tree rooted at node (a DocumentNode or any mapping root),
// without decoding the tree into a map and re-encoding it.
//
// Only the Value and Tag fields of each target scalar node are changed.
// The node's comments (head, line, foot), its style, and its anchor
// identity are preserved, and because the node is mutated in place, every
// alias node referencing an updated anchor keeps pointing at the very
// same *Node and therefore observes the new value.
//
// The update list is atomic: every path is resolved and every value is
// validated before any node is touched. If any entry fails (unknown
// path, duplicate key, alias traversal, non-mapping segment, non-scalar
// target, conflicting targets, or unsupported value type), an error
// wrapping one of the ErrUpdate* sentinels is returned and the node
// graph is left completely unmodified.
//
// Supported scope:
//   - Paths traverse mappings only; sequences are not addressable.
//   - The final node of a path must be a scalar.
//   - Alias nodes are never traversed or updated (ErrUpdateAliasTraversal).
//   - Duplicate keys matching a path segment are rejected
//     (ErrUpdateDuplicateKey) instead of picking one silently.
//
// Example, before:
//
//	server:
//	  # listen port
//	  port: &port 80 # primary port
//	backup:
//	  port: *port
//
// after UpdateScalars(doc, ScalarUpdate{Path: []string{"server", "port"}, Value: "auto"}),
// re-serialized with the default 4-space indent:
//
//	server:
//	    # listen port
//	    port: &port auto # primary port
//	backup:
//	    port: *port
//
// The comment, the plain style, and the &port anchor survive; the tag
// becomes !!str; and the *port alias still points at the same node, now
// holding "auto".
func UpdateScalars(node *Node, updates ...ScalarUpdate) error {
	if node == nil {
		return fmt.Errorf("yaml: UpdateScalars called on a nil node")
	}

	// Phase 1: resolve and validate everything, mutating nothing.
	type prepared struct {
		target *Node
		tag    string
		value  string
	}
	todos := make([]prepared, 0, len(updates))
	seen := make(map[*Node]int, len(updates))
	for i, u := range updates {
		tag, value, err := scalarRepresentation(u.Value)
		if err != nil {
			return fmt.Errorf("yaml: update %d (path %q): %w", i, u.Path, err)
		}
		target, err := resolveUpdatePath(node, u.Path)
		if err != nil {
			return fmt.Errorf("yaml: update %d (path %q): %w", i, u.Path, err)
		}
		if prev, ok := seen[target]; ok {
			return fmt.Errorf("yaml: updates %d and %d (path %q): %w",
				prev, i, u.Path, ErrUpdateConflict)
		}
		seen[target] = i
		todos = append(todos, prepared{target, tag, value})
	}

	// Phase 2: apply. Every entry is known to succeed at this point.
	for _, todo := range todos {
		todo.target.Tag = todo.tag
		todo.target.Value = todo.value
	}
	return nil
}

// resolveUpdatePath walks path from root and returns the final scalar
// node, applying the explicit rejection policies of UpdateScalars.
func resolveUpdatePath(root *Node, path []string) (*Node, error) {
	if len(path) == 0 {
		return nil, ErrUpdateEmptyPath
	}
	cur := root
	if cur.Kind == DocumentNode {
		if len(cur.Content) == 0 {
			return nil, fmt.Errorf("segment %q: %w", path[0], ErrUpdatePathNotFound)
		}
		cur = cur.Content[0]
	}
	for _, key := range path {
		if cur.Kind == AliasNode {
			return nil, fmt.Errorf("segment %q: %w", key, ErrUpdateAliasTraversal)
		}
		if cur.Kind != MappingNode {
			return nil, fmt.Errorf("segment %q: %w", key, ErrUpdateNotMapping)
		}
		var match *Node
		count := 0
		for j := 0; j+1 < len(cur.Content); j += 2 {
			k := cur.Content[j]
			if k.Kind != ScalarNode || shortTag(k.Tag) == mergeTag {
				continue
			}
			if k.Value == key {
				count++
				match = cur.Content[j+1]
			}
		}
		switch {
		case count > 1:
			return nil, fmt.Errorf("segment %q: %w", key, ErrUpdateDuplicateKey)
		case count == 0:
			return nil, fmt.Errorf("segment %q: %w", key, ErrUpdatePathNotFound)
		}
		cur = match
	}
	if cur.Kind == AliasNode {
		return nil, ErrUpdateAliasTraversal
	}
	if cur.Kind != ScalarNode {
		return nil, ErrUpdateNotScalar
	}
	return cur, nil
}

// scalarRepresentation converts a supported Go scalar into the YAML tag
// and plain representation stored on the target node, mirroring the
// formatting used by the encoder.
func scalarRepresentation(v interface{}) (tag, value string, err error) {
	switch v := v.(type) {
	case nil:
		return nullTag, "null", nil
	case string:
		return strTag, v, nil
	case bool:
		if v {
			return boolTag, "true", nil
		}
		return boolTag, "false", nil
	case int:
		return intTag, strconv.FormatInt(int64(v), 10), nil
	case int8:
		return intTag, strconv.FormatInt(int64(v), 10), nil
	case int16:
		return intTag, strconv.FormatInt(int64(v), 10), nil
	case int32:
		return intTag, strconv.FormatInt(int64(v), 10), nil
	case int64:
		return intTag, strconv.FormatInt(v, 10), nil
	case uint:
		return intTag, strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return intTag, strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return intTag, strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return intTag, strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return intTag, strconv.FormatUint(v, 10), nil
	case float32:
		return floatTag, formatFloat(float64(v), 32), nil
	case float64:
		return floatTag, formatFloat(v, 64), nil
	}
	return "", "", fmt.Errorf("%w: %T", ErrUpdateUnsupportedValue, v)
}

func formatFloat(f float64, precision int) string {
	s := strconv.FormatFloat(f, 'g', -1, precision)
	switch s {
	case "+Inf":
		return ".inf"
	case "-Inf":
		return "-.inf"
	case "NaN":
		return ".nan"
	}
	return s
}
