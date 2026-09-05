package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
)

// The name of the field that pads a record to the target size.
const fillerField = "_filler"

// The characters that fill the pad field. They are ASCII. JSON does not
// escape them. So one character adds exactly one byte to the JSON output.
const fillerChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomStringLen sets the length of a generated string value.
const randomStringLen = 12

// kind is the JSON type of one field in the plan.
type kind int

const (
	kindString kind = iota
	kindNumberInt
	kindNumberFloat
	kindBool
	kindNull
	kindObject
	kindArray
)

// node is one entry in the generation plan. It holds a JSON type. For an
// object it holds the child fields. For an array it holds the element plans.
type node struct {
	k        kind
	children []field // used when k is kindObject
	elements []*node // used when k is kindArray
}

// field is a named child of an object node.
type field struct {
	name string
	plan *node
}

// generator builds records from a plan. Each goroutine must use its own
// generator. It holds a private random source. So it is not shared.
type generator struct {
	plan            *node
	rnd             *rand.Rand
	minSize         int
	maxSize         int
	compressibility float64
}

// buildPlan reads a sample record file. It returns a generation plan.
func buildPlan(path string) (*node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sample file %q: %w", path, err)
	}
	var raw interface{}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse sample file %q: %w", path, err)
	}
	root := planFromValue(raw)
	if root.k != kindObject {
		return nil, fmt.Errorf("sample file %q must hold a JSON object at the top level", path)
	}
	return root, nil
}

// planFromValue turns one parsed JSON value into a plan node.
func planFromValue(v interface{}) *node {
	switch t := v.(type) {
	case map[string]interface{}:
		n := &node{k: kindObject}
		// Sort the keys. So the plan order is stable.
		keys := make([]string, 0, len(t))
		for key := range t {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			n.children = append(n.children, field{name: key, plan: planFromValue(t[key])})
		}
		return n
	case []interface{}:
		n := &node{k: kindArray}
		for _, e := range t {
			n.elements = append(n.elements, planFromValue(e))
		}
		return n
	case bool:
		return &node{k: kindBool}
	case json.Number:
		if _, err := t.Int64(); err == nil {
			return &node{k: kindNumberInt}
		}
		return &node{k: kindNumberFloat}
	case string:
		return &node{k: kindString}
	default:
		// This covers a JSON null value.
		return &node{k: kindNull}
	}
}

// newGenerator makes a generator for one goroutine. The seed makes each
// goroutine produce a different stream. The compressibility sets the target
// lz4 ratio of the filler. A value of 1 means random, incompressible data.
func newGenerator(plan *node, minSize, maxSize int, compressibility float64, seed int64) *generator {
	if compressibility < 1 {
		compressibility = 1
	}
	return &generator{
		plan:            plan,
		rnd:             rand.New(rand.NewSource(seed)),
		minSize:         minSize,
		maxSize:         maxSize,
		compressibility: compressibility,
	}
}

// value makes one random value for a plan node.
func (g *generator) value(n *node) interface{} {
	switch n.k {
	case kindString:
		return g.randString(randomStringLen)
	case kindNumberInt:
		return g.rnd.Int63n(1000000)
	case kindNumberFloat:
		return g.rnd.Float64() * 1000000
	case kindBool:
		return g.rnd.Intn(2) == 1
	case kindNull:
		return nil
	case kindObject:
		m := make(map[string]interface{}, len(n.children))
		for _, c := range n.children {
			m[c.name] = g.value(c.plan)
		}
		return m
	case kindArray:
		a := make([]interface{}, 0, len(n.elements))
		for _, e := range n.elements {
			a = append(a, g.value(e))
		}
		return a
	default:
		return nil
	}
}

// randString makes a random ASCII string of the given length. It draws 8
// bytes per RNG call. So it is fast enough for the per-message path.
func (g *generator) randString(size int) string {
	b := make([]byte, size)
	i := 0
	for i < size {
		v := g.rnd.Uint64()
		for j := 0; j < 8 && i < size; j++ {
			b[i] = fillerChars[byte(v)%byte(len(fillerChars))]
			v >>= 8
			i++
		}
	}
	return string(b)
}

// record makes one record. It pads the record to a random size in the
// range [minSize, maxSize]. It returns the JSON bytes.
func (g *generator) record() ([]byte, error) {
	// Pick the target size for this record.
	target := g.minSize
	if g.maxSize > g.minSize {
		target += g.rnd.Intn(g.maxSize - g.minSize + 1)
	}

	// Build the base object from the plan.
	m, ok := g.value(g.plan).(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("plan root is not an object")
	}

	// Set an empty filler first. Then measure the size.
	m[fillerField] = ""
	base, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	// One filler character adds one byte to the JSON. So the number of
	// needed characters is the gap between the base size and the target.
	need := target - len(base)
	if need < 0 {
		need = 0
	}
	m[fillerField] = g.compressibleFiller(need)

	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// compressibleFiller makes a filler string of the given size. It builds one
// random block of size (size / compressibility), then repeats that block to
// fill the size. lz4 compresses the repeats. So the ratio is about the
// compressibility value. A compressibility of 1 gives a fully random string.
func (g *generator) compressibleFiller(size int) string {
	if size <= 0 {
		return ""
	}
	if g.compressibility <= 1 {
		return g.randString(size)
	}
	unique := int(float64(size) / g.compressibility)
	if unique < 1 {
		unique = 1
	}
	block := g.randString(unique)
	b := make([]byte, 0, size)
	for len(b) < size {
		remaining := size - len(b)
		if remaining >= len(block) {
			b = append(b, block...)
		} else {
			b = append(b, block[:remaining]...)
		}
	}
	return string(b)
}
