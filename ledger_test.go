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

// Package yaml_test contains the field consumption ledger tests.
//
// Run the ledger tests with:
//
//	go test -run 'TestLedger|ExampleFieldLedger' -count=1 -v
package yaml_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLedgerStructAndUnknown verifies that struct fields are recorded as
// consumed and unmatched keys are recorded as unknown with positions.
func TestLedgerStructAndUnknown(t *testing.T) {
	type Config struct {
		Host string
		Port int
	}
	data := "host: localhost\nport: 8080\nunknown: value\n"

	var cfg Config
	ledger := &yaml.FieldLedger{}
	dec := yaml.NewDecoder(strings.NewReader(data))
	dec.SetLedger(ledger)
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries := ledger.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}

	byKey := map[string]yaml.FieldConsumption{}
	for _, e := range entries {
		byKey[e.Key] = e
	}

	if e, ok := byKey["host"]; !ok {
		t.Errorf("host not recorded")
	} else if e.Kind != yaml.ConsumedByStruct {
		t.Errorf("host kind = %v, want struct", e.Kind)
	} else if e.Node.Line != 1 {
		t.Errorf("host line = %d, want 1", e.Node.Line)
	}

	if e, ok := byKey["port"]; !ok {
		t.Errorf("port not recorded")
	} else if e.Kind != yaml.ConsumedByStruct {
		t.Errorf("port kind = %v, want struct", e.Kind)
	}

	if e, ok := byKey["unknown"]; !ok {
		t.Errorf("unknown not recorded")
	} else if e.Kind != yaml.NotConsumed {
		t.Errorf("unknown kind = %v, want unknown", e.Kind)
	} else if e.Node.Line != 3 {
		t.Errorf("unknown line = %d, want 3", e.Node.Line)
	}

	if !ledger.Done() {
		t.Errorf("ledger.Done() = false, want true")
	}
}

// TestLedgerCustomUnmarshalConsumesKeys verifies that keys consumed by a
// custom UnmarshalYAML are not reported as unknown.
func TestLedgerCustomUnmarshalConsumesKeys(t *testing.T) {
	data := "custom:\n  a: 1\n  b: 2\nunknown: x\n"

	var w struct {
		Custom *customUnmarshaler `yaml:"custom"`
	}
	ledger := &yaml.FieldLedger{}
	dec := yaml.NewDecoder(strings.NewReader(data))
	dec.SetLedger(ledger)
	if err := dec.Decode(&w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries := ledger.Entries()
	byKey := map[string]yaml.FieldConsumption{}
	for _, e := range entries {
		byKey[e.Key] = e
	}

	if e, ok := byKey["custom"]; !ok {
		t.Errorf("custom not recorded")
	} else if e.Kind != yaml.ConsumedByStruct {
		t.Errorf("custom kind = %v, want struct", e.Kind)
	}

	if e, ok := byKey["a"]; !ok {
		t.Errorf("a not recorded")
	} else if e.Kind != yaml.ConsumedByUnmarshal {
		t.Errorf("a kind = %v, want unmarshal", e.Kind)
	} else if len(e.Path) != 2 || e.Path[0] != "custom" || e.Path[1] != "a" {
		t.Errorf("a path = %v, want [custom a]", e.Path)
	}

	if e, ok := byKey["b"]; !ok {
		t.Errorf("b not recorded")
	} else if e.Kind != yaml.ConsumedByUnmarshal {
		t.Errorf("b kind = %v, want unmarshal", e.Kind)
	}

	if e, ok := byKey["unknown"]; !ok {
		t.Errorf("unknown not recorded")
	} else if e.Kind != yaml.NotConsumed {
		t.Errorf("unknown kind = %v, want unknown", e.Kind)
	}
}

type customUnmarshaler struct {
	fields map[string]string
}

func (c *customUnmarshaler) UnmarshalYAML(node *yaml.Node) error {
	c.fields = make(map[string]string)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		val := node.Content[i+1]
		c.fields[key.Value] = val.Value
		key.MarkConsumed()
	}
	return nil
}

// TestLedgerCallbackOnce verifies the OnDone callback is invoked exactly once.
func TestLedgerCallbackOnce(t *testing.T) {
	type Config struct {
		Host string
	}
	data := "host: localhost\n"

	var cfg Config
	ledger := &yaml.FieldLedger{}
	count := 0
	dec := yaml.NewDecoder(strings.NewReader(data))
	dec.SetLedger(ledger)
	dec.OnDone(func(l *yaml.FieldLedger) {
		count++
	})
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("callback count = %d, want 1", count)
	}
}

// TestLedgerFailurePreservesConsumption verifies that on failure the ledger
// retains prior consumption and does not mark success.
func TestLedgerFailurePreservesConsumption(t *testing.T) {
	type Config struct {
		A int
		B int
	}
	data := "a: 1\nb: not-an-int\nc: 3\n"

	var cfg Config
	ledger := &yaml.FieldLedger{}
	dec := yaml.NewDecoder(strings.NewReader(data))
	dec.SetLedger(ledger)
	err := dec.Decode(&cfg)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	if ledger.Done() {
		t.Errorf("ledger.Done() = true, want false")
	}
	if ledger.StoppedAt() == nil {
		t.Errorf("ledger.StoppedAt() = nil, want non-nil")
	} else if ledger.StoppedAt().Line != 2 {
		t.Errorf("stopped at line %d, want 2 (the failing value node)", ledger.StoppedAt().Line)
	}

	entries := ledger.Entries()
	byKey := map[string]yaml.FieldConsumption{}
	for _, e := range entries {
		byKey[e.Key] = e
	}

	if _, ok := byKey["a"]; !ok {
		t.Errorf("a not recorded (should be preserved after failure)")
	}
	if _, ok := byKey["b"]; !ok {
		t.Errorf("b not recorded (should be preserved after failure)")
	}
	if _, ok := byKey["c"]; ok {
		t.Errorf("c recorded (should not be recorded after failure)")
	}
}

// TestLedgerRenamedFieldReportsOriginalPosition verifies that a renamed field
// still reports the original input position.
func TestLedgerRenamedFieldReportsOriginalPosition(t *testing.T) {
	type Config struct {
		Host string `yaml:"hostname"`
	}
	data := "\n\nhostname: localhost\n"

	var cfg Config
	ledger := &yaml.FieldLedger{}
	dec := yaml.NewDecoder(strings.NewReader(data))
	dec.SetLedger(ledger)
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries := ledger.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Key != "hostname" {
		t.Errorf("key = %q, want hostname", e.Key)
	}
	if e.Node.Line != 3 {
		t.Errorf("line = %d, want 3 (original input position)", e.Node.Line)
	}
	if e.Kind != yaml.ConsumedByStruct {
		t.Errorf("kind = %v, want struct", e.Kind)
	}
}

// TestLedgerNestedMapAndArrayPaths verifies paths are recorded along the real
// node tree for nested maps and arrays.
func TestLedgerNestedMapAndArrayPaths(t *testing.T) {
	type Item struct {
		Name string
	}
	type Config struct {
		Items []Item
	}
	data := "items:\n  - name: foo\n  - name: bar\n"

	var cfg Config
	ledger := &yaml.FieldLedger{}
	dec := yaml.NewDecoder(strings.NewReader(data))
	dec.SetLedger(ledger)
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries := ledger.Entries()
	var namePaths [][]string
	for _, e := range entries {
		if e.Key == "name" {
			namePaths = append(namePaths, e.Path)
		}
	}
	if len(namePaths) != 2 {
		t.Fatalf("expected 2 name entries, got %d", len(namePaths))
	}
	want := [][]string{{"items", "0", "name"}, {"items", "1", "name"}}
	for i, p := range namePaths {
		if len(p) != len(want[i]) {
			t.Errorf("path[%d] = %v, want %v", i, p, want[i])
			continue
		}
		for j := range p {
			if p[j] != want[i][j] {
				t.Errorf("path[%d] = %v, want %v", i, p, want[i])
				break
			}
		}
	}
}

// TestLedgerDefaultDecoderNoLedger verifies that a decoder without a ledger
// does not record anything.
func TestLedgerDefaultDecoderNoLedger(t *testing.T) {
	type Config struct {
		Host string
	}
	data := "host: localhost\n"

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(data))
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want localhost", cfg.Host)
	}
}

// TestLedgerKnownFieldsStillRecords verifies that when KnownFields is enabled,
// unknown keys still appear in the ledger even though decoding returns an error.
func TestLedgerKnownFieldsStillRecords(t *testing.T) {
	type Config struct {
		Host string
	}
	data := "host: localhost\nunknown: value\n"

	var cfg Config
	ledger := &yaml.FieldLedger{}
	dec := yaml.NewDecoder(strings.NewReader(data))
	dec.SetLedger(ledger)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err == nil {
		t.Fatalf("expected error from KnownFields, got nil")
	}

	entries := ledger.Entries()
	byKey := map[string]yaml.FieldConsumption{}
	for _, e := range entries {
		byKey[e.Key] = e
	}

	if e, ok := byKey["host"]; !ok {
		t.Errorf("host not recorded")
	} else if e.Kind != yaml.ConsumedByStruct {
		t.Errorf("host kind = %v, want struct", e.Kind)
	}
	if e, ok := byKey["unknown"]; !ok {
		t.Errorf("unknown not recorded")
	} else if e.Kind != yaml.NotConsumed {
		t.Errorf("unknown kind = %v, want unknown", e.Kind)
	}
	if ledger.Done() {
		t.Errorf("ledger.Done() = true, want false (KnownFields error)")
	}
}

// ExampleFieldLedger demonstrates how to attach a field consumption ledger
// to a decoder and inspect the report after decoding a small config.
func ExampleFieldLedger() {
	type Config struct {
		Host string
		Port int
	}
	data := []byte("host: localhost\nport: 8080\nunknown: value\n")

	var cfg Config
	ledger := &yaml.FieldLedger{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.SetLedger(ledger)
	dec.OnDone(func(l *yaml.FieldLedger) {
		for _, e := range l.Entries() {
			fmt.Printf("%s -> %s (line %d)\n", e.Key, e.Kind, e.Node.Line)
		}
	})
	if err := dec.Decode(&cfg); err != nil {
		fmt.Println("error:", err)
		return
	}
	// Output:
	// host -> struct (line 1)
	// port -> struct (line 2)
	// unknown -> unknown (line 3)
}