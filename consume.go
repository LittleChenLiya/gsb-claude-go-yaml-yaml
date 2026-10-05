package yaml

import (
	"strconv"
	"strings"
	"sync"
)

// This file implements an opt-in field consumption ledger for decoding.
// A default Decoder keeps no ledger and pays no tracking cost; the
// ledger is only enabled via Decoder.TrackConsumption.
//
// Focused tests and the small config example can be run with:
//
//	go test -run 'Consumption' .
//	go test -run 'ExampleDecoder_TrackConsumption' .

// ConsumptionKind describes how a mapping key found in the decoded
// YAML document was treated by the decoder.
type ConsumptionKind int

const (
	// KeyUnconsumed marks a mapping key that was not consumed by any
	// struct field, map entry, or custom unmarshaler during decoding.
	KeyUnconsumed ConsumptionKind = iota

	// KeyStructField marks a mapping key consumed by a struct field,
	// including keys captured by an inline map field.
	KeyStructField

	// KeyMapKey marks a mapping key consumed into a generic map value.
	KeyMapKey

	// KeyCustomUnmarshaler marks a mapping key explicitly consumed by
	// a custom UnmarshalYAML implementation via ConsumeKeys.
	KeyCustomUnmarshaler
)

// String returns a human-readable name for the consumption kind.
func (k ConsumptionKind) String() string {
	switch k {
	case KeyUnconsumed:
		return "unconsumed"
	case KeyStructField:
		return "struct-field"
	case KeyMapKey:
		return "map-key"
	case KeyCustomUnmarshaler:
		return "custom-unmarshaler"
	}
	return "unknown"
}

// KeyConsumption records the fate of a single mapping key found in the
// decoded document. The position and node identity are taken from the
// actual key node of the parsed document, not inferred from the final
// decoded value, so a renamed struct field still reports the position
// of the original input key.
type KeyConsumption struct {
	// Path is the dotted path of literal mapping keys leading to this
	// key, with sequence indices in brackets, e.g. "servers[1].host".
	Path string

	// Key is the literal key as written in the input document.
	Key string

	// Line and Column hold the position of the key in the input.
	Line   int
	Column int

	// Kind reports how the key was consumed.
	Kind ConsumptionKind

	// Node is the actual key node in the parsed document.
	Node *Node
}

// ConsumptionReport summarizes how the mapping keys of a decoded
// document were consumed. It is passed to the callback registered with
// Decoder.TrackConsumption exactly once per Decode call.
type ConsumptionReport struct {
	// Keys lists every tracked mapping key in document order.
	Keys []KeyConsumption

	// Succeeded is the success marker, set only when Decode returned
	// a nil error. On failure it is left unset, while the consumption
	// recorded before the failure is preserved in Keys.
	Succeeded bool

	// Failed reports whether Decode returned an error.
	Failed bool

	// StopNode is the actual node being decoded when a failure stopped
	// the decoding. It is nil when decoding succeeded.
	StopNode *Node
}

// Unconsumed returns the subset of key entries that nothing consumed.
func (r *ConsumptionReport) Unconsumed() []KeyConsumption {
	var out []KeyConsumption
	for _, k := range r.Keys {
		if k.Kind == KeyUnconsumed {
			out = append(out, k)
		}
	}
	return out
}

// consumeTracker is the per-decode ledger. It is only allocated when
// the decoder has a consumption callback registered.
type consumeTracker struct {
	path []interface{} // string keys and int sequence indices
	keys []KeyConsumption
	stop *Node
	cb   func(*ConsumptionReport)
}

func (t *consumeTracker) pathString(extra string) string {
	var sb strings.Builder
	for _, seg := range t.path {
		switch s := seg.(type) {
		case string:
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.WriteString(s)
		case int:
			sb.WriteByte('[')
			sb.WriteString(strconv.Itoa(s))
			sb.WriteByte(']')
		}
	}
	if extra != "" {
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(extra)
	}
	return sb.String()
}

func (t *consumeTracker) record(n *Node, kind ConsumptionKind) {
	t.keys = append(t.keys, KeyConsumption{
		Path:   t.pathString(n.Value),
		Key:    n.Value,
		Line:   n.Line,
		Column: n.Column,
		Kind:   kind,
		Node:   n,
	})
}

func (t *consumeTracker) finish(err error) {
	report := &ConsumptionReport{
		Keys:      t.keys,
		Succeeded: err == nil,
		Failed:    err != nil,
	}
	if err != nil {
		report.StopNode = t.stop
	}
	t.cb(report)
}

// TrackConsumption registers a callback that receives a consumption
// report for every Decode call. The callback is invoked exactly once
// per Decode, whether decoding succeeds or fails. On failure the report
// preserves the consumption recorded up to the real stop node and does
// not set the Succeeded marker.
//
// A default Decoder keeps no ledger; tracking only happens after this
// method is called. Use TrackConsumption(nil) to disable it again.
func (dec *Decoder) TrackConsumption(cb func(*ConsumptionReport)) {
	dec.consumeCb = cb
}

// consumeNodes maps nodes currently being handled by a custom
// UnmarshalYAML to the tracker of the active decode, so that
// ConsumeKeys can find the ledger from the node identity alone.
var consumeNodes = struct {
	sync.Mutex
	m map[*Node]*consumeTracker
}{m: make(map[*Node]*consumeTracker)}

func registerConsumeNode(n *Node, t *consumeTracker) {
	consumeNodes.Lock()
	consumeNodes.m[n] = t
	consumeNodes.Unlock()
}

func unregisterConsumeNode(n *Node) {
	consumeNodes.Lock()
	delete(consumeNodes.m, n)
	consumeNodes.Unlock()
}

// ConsumeKeys may be called from a custom UnmarshalYAML implementation
// to declare that the named literal keys of the mapping node n were
// consumed by the custom logic. Consumed keys are reported with kind
// KeyCustomUnmarshaler instead of showing up as unknown or unconsumed.
// It is a no-op when the active decoder has no consumption tracking
// enabled, and returns whether a ledger recorded the keys.
func ConsumeKeys(n *Node, keys ...string) bool {
	if n == nil {
		return false
	}
	consumeNodes.Lock()
	t := consumeNodes.m[n]
	consumeNodes.Unlock()
	if t == nil {
		return false
	}
	for _, key := range keys {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				t.record(n.Content[i], KeyCustomUnmarshaler)
				break
			}
		}
	}
	return true
}
