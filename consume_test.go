package yaml

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
)

func findConsumption(r *ConsumptionReport, path string) *KeyConsumption {
	for i := range r.Keys {
		if r.Keys[i].Path == path {
			return &r.Keys[i]
		}
	}
	return nil
}

// Struct fields, renamed fields, nested maps, array paths, and
// unconsumed keys are all reported along real node identity.
func TestConsumptionStructAndPaths(t *testing.T) {
	const doc = `renamed: web
server:
  host: example.com
  port: 8080
servers:
- host: a
- host: b
extra: true
`
	type Server struct {
		Host string `yaml:"host"`
	}
	type Config struct {
		Name    string            `yaml:"renamed"`
		Server  map[string]string `yaml:"server"`
		Servers []Server          `yaml:"servers"`
	}
	var cfg Config
	var reports []*ConsumptionReport
	dec := NewDecoder(strings.NewReader(doc))
	dec.TrackConsumption(func(r *ConsumptionReport) { reports = append(reports, r) })
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("callback fired %d times, want 1", len(reports))
	}
	r := reports[0]
	if !r.Succeeded || r.Failed || r.StopNode != nil {
		t.Fatalf("bad success markers: %+v", r)
	}

	// Renamed field reports the original input key and position.
	renamed := findConsumption(r, "renamed")
	if renamed == nil {
		t.Fatal("missing entry for renamed key")
	}
	if renamed.Key != "renamed" || renamed.Kind != KeyStructField {
		t.Fatalf("bad renamed entry: %+v", renamed)
	}
	if renamed.Line != 1 {
		t.Fatalf("renamed key reported at line %d, want original input line 1", renamed.Line)
	}
	if renamed.Node == nil || renamed.Node.Value != "renamed" || renamed.Node.Line != renamed.Line {
		t.Fatalf("renamed entry not recorded along real node identity: %+v", renamed)
	}

	// Nested map keys carry dotted paths.
	host := findConsumption(r, "server.host")
	if host == nil || host.Kind != KeyMapKey || host.Line != 3 {
		t.Fatalf("bad nested map entry: %+v", host)
	}

	// Array paths use indices along the real nodes.
	second := findConsumption(r, "servers[1].host")
	if second == nil || second.Kind != KeyStructField || second.Line != 7 {
		t.Fatalf("bad array path entry: %+v", second)
	}

	// Unmatched keys are reported unconsumed with their position.
	extra := findConsumption(r, "extra")
	if extra == nil || extra.Kind != KeyUnconsumed || extra.Line != 8 {
		t.Fatalf("bad unconsumed entry: %+v", extra)
	}
	if got := r.Unconsumed(); len(got) != 1 || got[0].Key != "extra" {
		t.Fatalf("Unconsumed() = %+v", got)
	}
}

type trackedSection struct {
	A int
	B int
}

func (s *trackedSection) UnmarshalYAML(n *Node) error {
	ConsumeKeys(n, "a", "b")
	var raw struct {
		A int `yaml:"a"`
		B int `yaml:"b"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	s.A, s.B = raw.A, raw.B
	return nil
}

// Keys consumed by a custom UnmarshalYAML are not reported unknown,
// plain unmatched keys keep positions, and the callback fires once.
func TestConsumptionCustomUnmarshaler(t *testing.T) {
	const doc = `wrapped:
  a: 1
  b: 2
loose: true
`
	var cfg struct {
		W trackedSection `yaml:"wrapped"`
	}
	calls := 0
	var report *ConsumptionReport
	dec := NewDecoder(strings.NewReader(doc))
	dec.TrackConsumption(func(r *ConsumptionReport) {
		calls++
		report = r
	})
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("callback fired %d times, want 1", calls)
	}
	if cfg.W.A != 1 || cfg.W.B != 2 {
		t.Fatalf("custom section not decoded: %+v", cfg.W)
	}
	for _, path := range []string{"wrapped.a", "wrapped.b"} {
		k := findConsumption(report, path)
		if k == nil || k.Kind != KeyCustomUnmarshaler {
			t.Fatalf("custom-consumed key %q reported as: %+v", path, k)
		}
	}
	for _, k := range report.Unconsumed() {
		if k.Key == "a" || k.Key == "b" {
			t.Fatalf("custom-consumed key %q reported unknown", k.Key)
		}
	}
	loose := findConsumption(report, "loose")
	if loose == nil || loose.Kind != KeyUnconsumed || loose.Line != 4 {
		t.Fatalf("plain unmatched key has no position: %+v", loose)
	}
}

type failingSection struct{}

func (f *failingSection) UnmarshalYAML(n *Node) error {
	return errors.New("boom")
}

// A failure preserves the consumption recorded so far, does not write
// the success marker, and reports the real stop node.
func TestConsumptionFailure(t *testing.T) {
	const doc = `first: 1
second:
  x: 1
third: 3
`
	var cfg struct {
		First  int            `yaml:"first"`
		Second failingSection `yaml:"second"`
	}
	calls := 0
	var report *ConsumptionReport
	dec := NewDecoder(strings.NewReader(doc))
	dec.TrackConsumption(func(r *ConsumptionReport) {
		calls++
		report = r
	})
	if err := dec.Decode(&cfg); err == nil {
		t.Fatal("Decode unexpectedly succeeded")
	}
	if calls != 1 {
		t.Fatalf("callback fired %d times, want 1", calls)
	}
	if report.Succeeded || !report.Failed {
		t.Fatalf("failure wrote success marker: %+v", report)
	}
	// Consumption recorded before the failure is preserved.
	first := findConsumption(report, "first")
	if first == nil || first.Kind != KeyStructField {
		t.Fatalf("prior consumption lost: %+v", report.Keys)
	}
	// The failure is reported at the real stop node: the mapping node
	// handed to the failing custom unmarshaler, starting at line 3.
	if report.StopNode == nil || report.StopNode.Kind != MappingNode || report.StopNode.Line != 3 {
		t.Fatalf("stop node = %+v, want the mapping node at line 3", report.StopNode)
	}
	// Keys after the stop node were never reached.
	if k := findConsumption(report, "third"); k != nil {
		t.Fatalf("key past the stop node was tracked: %+v", k)
	}
}

// The default Decoder keeps no ledger and works unchanged.
func TestConsumptionDefaultDecoderNoLedger(t *testing.T) {
	var cfg struct {
		Name string `yaml:"name"`
	}
	dec := NewDecoder(strings.NewReader("name: web\n"))
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if cfg.Name != "web" {
		t.Fatalf("decoded %+v", cfg)
	}
}

func ExampleDecoder_TrackConsumption() {
	const config = `
name: web
listen: ":8080"
debug: true
`
	type Config struct {
		Name   string `yaml:"name"`
		Listen string `yaml:"listen"`
	}
	var cfg Config
	dec := NewDecoder(strings.NewReader(config))
	dec.TrackConsumption(func(r *ConsumptionReport) {
		for _, k := range r.Unconsumed() {
			fmt.Printf("unconsumed key %q at line %d\n", k.Key, k.Line)
		}
		fmt.Println("succeeded:", r.Succeeded)
	})
	if err := dec.Decode(&cfg); err != nil {
		log.Fatal(err)
	}
	// Output:
	// unconsumed key "debug" at line 4
	// succeeded: true
}
