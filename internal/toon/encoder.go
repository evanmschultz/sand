// Package toon implements a minimal TOON (Token-Oriented Object Notation)
// encoder covering the subset SAND-SPEC §3.1 / §4 requires for sand response
// shapes.
//
// Supported features (per Context7 /toon-format/toon and SAND-SPEC §3.1):
//
//   - Top-level object: ordered key/value pairs, one per line.
//   - Nested object fields under indentation (two spaces per depth).
//   - Primitive scalars: string, int, int64, float64, bool, and nil.
//   - Inline primitive arrays: `key[N]: v1,v2,v3` for slices of primitives.
//   - Tabular arrays: `key[N]{field1,field2}:` header followed by bare CSV
//     rows; length is required even for empty arrays (`key[0]{...}:`).
//   - Block scalars (`|` literal) for multi-line strings; content is indented
//     one level deeper than the parent key.
//   - CSV quoting of values containing the active delimiter (`,`), embedded
//     quotes, or leading/trailing whitespace; embedded `"` is doubled.
//
// Out of scope (YAGNI per SAND-SPEC §4.5):
//
//   - Decoder.
//   - Delimiter switching (tab / pipe).
//   - Anchors / references.
//   - Reflection over arbitrary Go structs; callers shape input using the
//     Object / Tabular / Inline / Block helper types declared below.
package toon

import (
	"fmt"
	"strconv"
	"strings"
)

// Object is an ordered sequence of key/value pairs. Callers use this rather
// than map[string]any so that emission order is deterministic and matches the
// SAND-SPEC §3.1 golden example byte-for-byte.
type Object []Field

// Field is one key/value entry inside an Object.
type Field struct {
	Key   string
	Value any
}

// Tabular is a uniform array of rows sharing the field set declared in
// Fields. Each row's values align positionally with Fields. Length is emitted
// in the header even when len(Rows) == 0 per the TOON spec.
type Tabular struct {
	Fields []string
	Rows   [][]any
}

// Inline is a primitive array emitted on a single line as `key[N]: v1,v2,v3`.
// Element types must be encodable as scalars (string / int / int64 / float64 /
// bool / nil); using a non-scalar element returns an error from Encode.
type Inline []any

// Block forces a string value to be emitted as a `|` block scalar even if it
// has no newline. Multi-line strings are auto-detected and use a block
// scalar without needing this wrapper, but Block is useful for free-form text
// fields like SAND-SPEC §3.1's `result` where the convention is to always
// use a block scalar.
type Block string

// indentUnit is the per-level indentation prefix (two spaces).
const indentUnit = "  "

// Encode serializes v into TOON text per the subset described in the package
// doc. v MUST be an Object at the top level (SAND-SPEC §3.1 calls for a flat
// object root, no array wrapping).
func Encode(v any) ([]byte, error) {
	obj, ok := v.(Object)
	if !ok {
		return nil, fmt.Errorf("toon: top-level value must be Object, got %T", v)
	}

	var b strings.Builder
	if err := emitObject(&b, obj, 0); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// emitObject writes the key/value pairs of obj at the given indentation depth.
func emitObject(b *strings.Builder, obj Object, depth int) error {
	prefix := strings.Repeat(indentUnit, depth)
	for _, f := range obj {
		if err := emitField(b, prefix, depth, f.Key, f.Value); err != nil {
			return fmt.Errorf("toon: field %q: %w", f.Key, err)
		}
	}
	return nil
}

// emitField writes one key/value pair, dispatching on value type.
func emitField(b *strings.Builder, prefix string, depth int, key string, val any) error {
	switch v := val.(type) {
	case Object:
		b.WriteString(prefix)
		b.WriteString(key)
		b.WriteString(":\n")
		return emitObject(b, v, depth+1)

	case Tabular:
		return emitTabular(b, prefix, depth, key, v)

	case Inline:
		return emitInline(b, prefix, key, v)

	case Block:
		return emitBlockScalar(b, prefix, depth, key, string(v))

	case string:
		// Auto-promote multi-line strings to a block scalar; the alternative
		// (quoting + escaping newlines) loses readability for the `result`
		// field that motivates this path.
		if strings.ContainsRune(v, '\n') {
			return emitBlockScalar(b, prefix, depth, key, v)
		}
		b.WriteString(prefix)
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(encodeScalarString(v))
		b.WriteByte('\n')
		return nil

	default:
		s, err := scalarString(val)
		if err != nil {
			return err
		}
		b.WriteString(prefix)
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(s)
		b.WriteByte('\n')
		return nil
	}
}

// emitTabular writes a `key[N]{f1,f2}:` header followed by bare CSV rows.
// Length is always present, even when len(Rows) == 0.
func emitTabular(b *strings.Builder, prefix string, depth int, key string, t Tabular) error {
	b.WriteString(prefix)
	b.WriteString(key)
	b.WriteByte('[')
	b.WriteString(strconv.Itoa(len(t.Rows)))
	b.WriteString("]{")
	b.WriteString(strings.Join(t.Fields, ","))
	b.WriteString("}:\n")

	rowPrefix := strings.Repeat(indentUnit, depth+1)
	for i, row := range t.Rows {
		if len(row) != len(t.Fields) {
			return fmt.Errorf("toon: tabular %q row %d has %d values, want %d", key, i, len(row), len(t.Fields))
		}
		b.WriteString(rowPrefix)
		for j, cell := range row {
			if j > 0 {
				b.WriteByte(',')
			}
			s, err := scalarString(cell)
			if err != nil {
				return fmt.Errorf("toon: tabular %q row %d col %d: %w", key, i, j, err)
			}
			b.WriteString(s)
		}
		b.WriteByte('\n')
	}
	return nil
}

// emitInline writes a primitive array as `key[N]: v1,v2,v3`.
func emitInline(b *strings.Builder, prefix, key string, arr Inline) error {
	b.WriteString(prefix)
	b.WriteString(key)
	b.WriteByte('[')
	b.WriteString(strconv.Itoa(len(arr)))
	b.WriteString("]: ")
	for i, v := range arr {
		if i > 0 {
			b.WriteByte(',')
		}
		s, err := scalarString(v)
		if err != nil {
			return fmt.Errorf("toon: inline %q elem %d: %w", key, i, err)
		}
		b.WriteString(s)
	}
	b.WriteByte('\n')
	return nil
}

// emitBlockScalar writes `key: |` followed by the value's lines, each
// indented one level deeper than the parent key.
func emitBlockScalar(b *strings.Builder, prefix string, depth int, key, val string) error {
	b.WriteString(prefix)
	b.WriteString(key)
	b.WriteString(": |\n")

	body := strings.Repeat(indentUnit, depth+1)
	// Split on '\n' so an empty value still produces one empty content line,
	// matching the YAML-style block-scalar convention TOON inherits.
	for _, line := range strings.Split(val, "\n") {
		b.WriteString(body)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return nil
}

// scalarString renders one primitive value as a CSV / inline cell, applying
// TOON quoting rules. Non-scalar inputs return an error.
func scalarString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return encodeScalarString(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("toon: unsupported scalar type %T", v)
	}
}

// encodeScalarString applies TOON quoting rules to a string value.
//
// Per Context7 /toon-format/toon (Syntax Cheatsheet > Quoting Rules Summary),
// strings need quotes when they:
//   - are empty
//   - have leading or trailing whitespace
//   - contain the active delimiter (`,`)
//   - contain quotes (`"`) or newlines (newlines are normally handled
//     upstream by promoting to a block scalar; quoting here is a safety net)
//
// Structural characters like `:`, `[`, `]`, `{`, `}` only need quoting in
// header position (the encoder never builds keys from user input — it
// receives them as Object.Field.Key). In value position they pass through
// bare, which matches the SAND-SPEC §3.1 golden example for values like
// `claude-native:opus` and `/tmp/sand-dispatch/log/abc123.json`.
//
// Embedded `"` is doubled per CSV convention.
func encodeScalarString(s string) string {
	if s == "" {
		return `""`
	}
	if needsQuoting(s) {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// needsQuoting reports whether s requires CSV / scalar quoting under TOON's
// minimal-quoting rule for VALUE position. Header / key position has its own
// rules, but sand never derives keys from runtime input so this function
// only handles values.
func needsQuoting(s string) bool {
	if s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' {
		return true
	}
	for _, r := range s {
		switch r {
		case ',', '"', '\n', '\r':
			return true
		}
	}
	return false
}
