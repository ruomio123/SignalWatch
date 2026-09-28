package generation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Schema is the closed JSON subset used by fixed workflow output DTOs. The
// same contract drives provider requests, prompts and local validation.
type Schema struct {
	Type                 string             `json:"type"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
}

// Structural returns an independent projection onto the structural constraints
// accepted by providers such as Qwen. The complete schema remains unchanged for
// prompts and server validation; text sizes are checked separately in UTF-8 bytes.
func (s *Schema) Structural() *Schema {
	if s == nil {
		return nil
	}
	out := *s
	out.MinItems, out.MaxItems = nil, nil
	out.Required = append([]string(nil), s.Required...)
	out.Enum = append([]string(nil), s.Enum...)
	if s.AdditionalProperties != nil {
		v := *s.AdditionalProperties
		out.AdditionalProperties = &v
	}
	if s.Properties != nil {
		out.Properties = make(map[string]*Schema, len(s.Properties))
		for key, child := range s.Properties {
			out.Properties[key] = child.Structural()
		}
	}
	out.Items = s.Items.Structural()
	return &out
}

func SchemaFor[T any]() *Schema { return schemaType(reflect.TypeFor[T]()) }
func schemaType(t reflect.Type) *Schema {
	if t.Kind() == reflect.Pointer {
		return schemaType(t.Elem())
	}
	s := &Schema{}
	switch t.Kind() {
	case reflect.String:
		s.Type = "string"
	case reflect.Bool:
		s.Type = "boolean"
	case reflect.Slice:
		s.Type = "array"
		s.Items = schemaType(t.Elem())
	case reflect.Struct:
		no := false
		s.Type = "object"
		s.AdditionalProperties = &no
		s.Properties = map[string]*Schema{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := f.Tag.Get("json")
			if name == "" || strings.Contains(name, ",") || name == "-" {
				panic("output DTO fields require explicit, required JSON names")
			}
			child := schemaType(f.Type)
			if values := f.Tag.Get("enum"); values != "" {
				child.Enum = strings.Split(values, ",")
			}
			s.Properties[name] = child
			s.Required = append(s.Required, name)
		}
	default:
		panic("unsupported output DTO type: " + t.String())
	}
	return s
}
func (s *Schema) Instructions() string {
	raw, _ := json.Marshal(s)
	return "\nReturn exactly one JSON object conforming to this JSON Schema. All listed fields are required; do not add fields.\n" + string(raw)
}

// Validate reports only server-defined paths and rules, never output values or
// unknown field names (which can contain arbitrary model/user content).
func (s *Schema) Validate(raw []byte) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return &OutputError{Code: "output_invalid_json", Path: "$", Rule: "invalid_json"}
	}
	return s.validate(value, "$", 0)
}
func (s *Schema) validate(v any, path string, depth int) error {
	failure := func(rule string) error { return &OutputError{Code: "output_schema_mismatch", Path: path, Rule: rule} }
	if depth > 32 {
		return failure("too_deep")
	}
	switch s.Type {
	case "object":
		object, ok := v.(map[string]any)
		if !ok {
			return failure("expected_object")
		}
		for _, key := range s.Required {
			if _, ok := object[key]; !ok {
				return &OutputError{Code: "output_schema_mismatch", Path: path + "." + key, Rule: "required_field"}
			}
		}
		for key := range object {
			if _, ok := s.Properties[key]; !ok {
				return failure("unexpected_field")
			}
		}
		keys := make([]string, 0, len(s.Properties))
		for key := range s.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := s.Properties[key].validate(object[key], path+"."+key, depth+1); err != nil {
				return err
			}
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			return failure("expected_array")
		}
		for i, item := range items {
			if err := s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		count := len(items)
		if s.MinItems != nil && count < *s.MinItems {
			return &OutputError{Code: "output_schema_mismatch", Path: path, Rule: "min_items", Count: &count, Limit: s.MinItems, Unit: "items"}
		}
		if s.MaxItems != nil && count > *s.MaxItems {
			return &OutputError{Code: "output_limit_exceeded", Path: path, Rule: "max_items", Count: &count, Limit: s.MaxItems, Unit: "items"}
		}
	case "string":
		text, ok := v.(string)
		if !ok {
			return failure("expected_string")
		}
		if len(s.Enum) > 0 {
			for _, allowed := range s.Enum {
				if text == allowed {
					return nil
				}
			}
			return failure("invalid_enum")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return failure("expected_boolean")
		}
	}
	return nil
}
