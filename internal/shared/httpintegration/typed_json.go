package httpintegration

import (
	"encoding/json"
	"errors"
)

// outputBudget is shared across all rows, metadata, nested collections and
// repeated references. Limits apply to the expanded output, not just the input.
type outputBudget struct{ nodes, bytes int }

func (b *outputBudget) node(depth, size int) error {
	b.nodes++
	b.bytes += size
	if depth > maxDepth || b.nodes > maxNodes || b.bytes > maxBytes {
		return errors.New("expanded output exceeds limit")
	}
	return nil
}
func (b *outputBudget) key(key string) error {
	raw, _ := json.Marshal(key)
	b.bytes += len(raw) + 2
	if b.bytes > maxBytes {
		return errors.New("expanded output exceeds limit")
	}
	return nil
}
func (b *outputBudget) add(v any, depth int) error {
	switch x := v.(type) {
	case map[string]any:
		if err := b.node(depth, 2); err != nil {
			return err
		}
		for k, child := range x {
			if err := b.key(k); err != nil {
				return err
			}
			if err := b.add(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if err := b.node(depth, 2+len(x)); err != nil {
			return err
		}
		for _, child := range x {
			if err := b.add(child, depth+1); err != nil {
				return err
			}
		}
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return errors.New("invalid expanded value")
		}
		return b.node(depth, len(raw))
	}
	return nil
}

// Full-string placeholders in JSONBody are opaque strings by default. Only
// quantity, page, page_size, offset, include_inactive and manual_form_data may
// decode valid JSON into typed values. Invalid JSON remains a string; valid JSON
// exceeding structural limits fails. Embedded placeholders and legacy Body stay
// strings. Literal JSON types are unchanged.
func typedJSONVariable(name string) bool {
	switch name {
	case "quantity", "page", "page_size", "offset", "include_inactive", "manual_form_data":
		return true
	default:
		return false
	}
}

func expandJSON(v any, vars map[string]string, budget *outputBudget, depth int) (any, error) {
	switch x := v.(type) {
	case string:
		if placeholder.FindString(x) == x && x != "" {
			name := x[2 : len(x)-2]
			raw, ok := vars[name]
			if !ok {
				return nil, errors.New("missing variable or invalid template")
			}
			var value any = raw
			if typedJSONVariable(name) {
				decoded, err := decodeJSON([]byte(raw))
				if err != nil {
					// Do not turn valid but over-budget JSON into a bypass.
					if json.Valid([]byte(raw)) {
						return nil, err
					}
				} else {
					value = decoded
				}
			}
			if err := budget.add(value, depth); err != nil {
				return nil, err
			}
			return value, nil
		}
		value, err := substitute(x, vars, false)
		if err != nil {
			return nil, err
		}
		if err := budget.add(value, depth); err != nil {
			return nil, err
		}
		return value, nil
	case map[string]any:
		if err := budget.node(depth, 2); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(x))
		for k, child := range x {
			if err := budget.key(k); err != nil {
				return nil, err
			}
			value, err := expandJSON(child, vars, budget, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = value
		}
		return out, nil
	case []any:
		if err := budget.node(depth, 2+len(x)); err != nil {
			return nil, err
		}
		out := make([]any, len(x))
		for i, child := range x {
			value, err := expandJSON(child, vars, budget, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	default:
		if err := budget.add(v, depth); err != nil {
			return nil, err
		}
		return v, nil
	}
}
