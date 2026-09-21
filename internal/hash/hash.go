package hash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// CanonicalJSON returns JSON with sorted object keys and no insignificant whitespace.
func CanonicalJSON(v any) []byte {
	var buf bytes.Buffer
	writeCanonical(&buf, toCanonical(v))
	return buf.Bytes()
}

// SHA256Hex returns the lowercase hex sha256 over the concatenated parts.
func SHA256Hex(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// toCanonical converts decoded JSON values into a form with deterministic key order.
func toCanonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, [2]any{k, toCanonical(t[k])})
		}
		return orderedMap(out)
	case []any:
		for i := range t {
			t[i] = toCanonical(t[i])
		}
		return t
	default:
		return v
	}
}

type orderedMap [][2]any

func (m orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, kv := range m {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(kv[0].(string))
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		val, err := marshalCanonical(kv[1])
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshalCanonical(v any) ([]byte, error) {
	switch t := v.(type) {
	case orderedMap:
		return t.MarshalJSON()
	default:
		return json.Marshal(t)
	}
}

func writeCanonical(buf *bytes.Buffer, v any) {
	out, err := marshalCanonical(v)
	if err != nil {
		fmt.Fprintf(buf, "null")
		return
	}
	// Ensure the input round-trips through canonical form: re-decode with
	// UseNumber so large numbers are preserved, then encode canonically.
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		buf.Write(out)
		return
	}
	canonical := toCanonical(decoded)
	enc, err := marshalCanonical(canonical)
	if err != nil {
		buf.Write(out)
		return
	}
	buf.Write(enc)
}
