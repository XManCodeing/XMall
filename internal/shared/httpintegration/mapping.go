package httpintegration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// MapResponse maps only explicitly configured fields. Decimal transformations
// return decimal.Decimal; int returns int64. Optional absent fields are omitted.
// Enums are exhaustive: an unknown source value is an error, never a fallback.
func MapResponse(ep Endpoint, raw []byte) (map[string]any, error) {
	if err := validateMapping(ep); err != nil {
		return nil, err
	}
	root, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	if ep.Success.Path != "" {
		v, ok := lookup(root, ep.Success.Path)
		if !ok {
			return nil, errors.New("success condition missing")
		}
		expected, _ := ep.Success.equals.get(ep.Success.Equals)
		if !matchesScalar(v, expected) {
			return nil, errors.New("upstream success condition not satisfied")
		}
	}
	budget := &outputBudget{}
	if ep.List || ep.ListPath != "" {
		v, ok := lookup(root, ep.ListPath)
		if !ok {
			return nil, errors.New("list path missing")
		}
		list, ok := v.([]any)
		if !ok {
			return nil, errors.New("list path is not an array")
		}
		result, err := mapFieldsBounded(ep.MetaFields, root, budget, 0)
		if err != nil {
			return nil, err
		}
		if err := budget.key("items"); err != nil {
			return nil, err
		}
		if err := budget.node(1, 2+len(list)); err != nil {
			return nil, err
		}
		items := make([]any, 0, len(list))
		for _, item := range list {
			m, err := mapItem(ep, item, budget, 2)
			if err != nil {
				return nil, err
			}
			items = append(items, m)
		}
		result["items"] = items
		return result, nil
	}
	return mapItem(ep, root, budget, 0)
}

func mapItem(ep Endpoint, root any, budget *outputBudget, depth int) (map[string]any, error) {
	result, err := mapFieldsBounded(ep.Fields, root, budget, depth)
	if err != nil {
		return nil, err
	}
	for name, collection := range ep.Collections {
		value, exists := lookup(root, collection.Path)
		if !exists {
			return nil, errors.New("collection path missing")
		}
		list, ok := value.([]any)
		if !ok {
			return nil, errors.New("collection path is not an array")
		}
		if err := budget.key(name); err != nil {
			return nil, err
		}
		if err := budget.node(depth+1, 2+len(list)); err != nil {
			return nil, err
		}
		items := make([]any, 0, len(list))
		for _, item := range list {
			mapped, err := mapFieldsBounded(collection.Fields, item, budget, depth+2)
			if err != nil {
				return nil, err
			}
			items = append(items, mapped)
		}
		result[name] = items
	}
	return result, nil
}

func decodeJSON(raw []byte) (any, error) {
	if len(raw) > maxBytes {
		return nil, errors.New("JSON exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	v, err := readJSON(d, 0, &nodes)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing or malformed JSON")
	}
	return v, nil
}
func readJSON(d *json.Decoder, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > maxDepth || *nodes > maxNodes {
		return nil, errors.New("JSON complexity limit exceeded")
	}
	t, err := d.Token()
	if err != nil {
		return nil, errors.New("invalid JSON")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, errors.New("invalid JSON object")
			}
			s, ok := k.(string)
			if !ok {
				return nil, errors.New("invalid JSON object key")
			}
			if _, ok := m[s]; ok {
				return nil, errors.New("duplicate JSON object key")
			}
			v, err := readJSON(d, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			m[s] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid JSON object")
		}
		return m, nil
	case '[':
		a := []any{}
		for d.More() {
			v, err := readJSON(d, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid JSON array")
		}
		return a, nil
	default:
		return nil, errors.New("invalid JSON delimiter")
	}
}
func lookup(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	for _, part := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = x[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}
func scalar(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return string(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	default:
		return "", errors.New("expected scalar value")
	}
}
func mapFields(fields map[string]Field, root any) (map[string]any, error) {
	return mapFieldsBounded(fields, root, &outputBudget{}, 0)
}
func mapFieldsBounded(fields map[string]Field, root any, budget *outputBudget, depth int) (map[string]any, error) {
	if err := budget.node(depth, 2); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(fields))
	for name, f := range fields {
		var v any
		var exists bool
		if f.Path == "" {
			v, exists = f.value.get(f.Value)
		} else {
			v, exists = lookup(root, f.Path)
			// Preserve the historical fallback semantics for null source fields.
			exists = exists && v != nil
		}
		if !exists {
			v, exists = f.fallback.get(f.Default)
		}
		if !exists {
			if f.Required {
				return nil, errors.New("required mapped field missing")
			}
			continue
		}
		if len(f.Enum) > 0 {
			s, err := scalar(v)
			if err != nil {
				return nil, err
			}
			mapped, ok := f.Enum[s]
			if !ok {
				return nil, errors.New("unrecognized enum value")
			}
			v = mapped
		}
		mapped, err := transform(v, f.Transform)
		if err != nil {
			return nil, err
		}
		if err := budget.key(name); err != nil {
			return nil, err
		}
		if err := budget.add(mapped, depth+1); err != nil {
			return nil, err
		}
		result[name] = mapped
	}
	return result, nil
}
func transform(v any, kind string) (any, error) {
	if kind == "" {
		return v, nil
	}
	s, err := scalar(v)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "string":
		return s, nil
	case "int":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, errors.New("invalid int64 value")
		}
		return n, nil
	case "bool":
		switch s {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return nil, errors.New("boolean must be true or false")
		}
	case "decimal", "cents_to_units":
		// Bound exponents before any rescaling to avoid pathological allocation.
		if len(s) > 256 {
			return nil, errors.New("decimal exceeds precision limit")
		}
		d, err := decimal.NewFromString(s)
		if err != nil || d.Exponent() < -1000 || d.Exponent() > 1000 {
			return nil, errors.New("invalid or excessive decimal")
		}
		if kind == "cents_to_units" {
			if !d.Equal(d.Truncate(0)) {
				return nil, errors.New("cents must be integral")
			}
			return d.Shift(-2), nil
		}
		return d, nil
	default:
		return nil, fmt.Errorf("unsupported transform")
	}
}
