package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"
)

// TestRecordSizeInRange checks that each record hits a size in the range.
func TestRecordSizeInRange(t *testing.T) {
	plan, err := buildPlan("sample.json")
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}

	const minSize = 10240
	const maxSize = 102400
	gen := newGenerator(plan, minSize, maxSize, 1.0, 1)

	for i := 0; i < 200; i++ {
		b, err := gen.record()
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		if len(b) < minSize || len(b) > maxSize {
			t.Fatalf("size %d is out of the range [%d, %d]", len(b), minSize, maxSize)
		}
		// The record must be valid JSON.
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("record is not valid JSON: %v", err)
		}
		// The filler field must exist.
		if _, ok := m[fillerField]; !ok {
			t.Fatalf("record has no %q field", fillerField)
		}
		// The original fields must exist.
		for _, name := range []string{"id", "name", "active", "score", "tags", "meta", "note"} {
			if _, ok := m[name]; !ok {
				t.Fatalf("record has no %q field", name)
			}
		}
	}
}

// TestSmallTargetStillValid checks the case when the base record is near or
// over the target. The record must stay valid JSON.
func TestSmallTargetStillValid(t *testing.T) {
	plan, err := buildPlan("sample.json")
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	gen := newGenerator(plan, 1, 1, 1.0, 1)
	b, err := gen.record()
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
}

// TestCompressibleFiller checks that the compressibility control makes the
// filler compressible, and that a value of 1 stays incompressible.
func TestCompressibleFiller(t *testing.T) {
	plan, err := buildPlan("sample.json")
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	const size = 40960
	gzipLen := func(b []byte) int {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
		return buf.Len()
	}

	// Compressibility 4 should shrink well under half the original size.
	genC := newGenerator(plan, size, size, 4.0, 1)
	rc, err := genC.record()
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if c := gzipLen(rc); c > len(rc)/2 {
		t.Fatalf("compressibility=4: compressed %d not < half of %d", c, len(rc))
	}

	// Compressibility 1 is random. gzip entropy coding still shrinks a
	// 62-symbol alphabet to about 75%, but it must not halve.
	genR := newGenerator(plan, size, size, 1.0, 1)
	rr, err := genR.record()
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if c := gzipLen(rr); c < len(rr)/2 {
		t.Fatalf("compressibility=1: compressed %d shrank below half of %d", c, len(rr))
	}
}

// TestPlanTypes checks that the plan reads the JSON types from the sample.
func TestPlanTypes(t *testing.T) {
	plan, err := buildPlan("sample.json")
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if plan.k != kindObject {
		t.Fatalf("root is not an object")
	}
	want := map[string]kind{
		"id":     kindNumberInt,
		"name":   kindString,
		"active": kindBool,
		"score":  kindNumberFloat,
		"tags":   kindArray,
		"meta":   kindObject,
		"note":   kindNull,
	}
	got := map[string]kind{}
	for _, c := range plan.children {
		got[c.name] = c.plan.k
	}
	for name, k := range want {
		if got[name] != k {
			t.Fatalf("field %q: got kind %d, want %d", name, got[name], k)
		}
	}
}
