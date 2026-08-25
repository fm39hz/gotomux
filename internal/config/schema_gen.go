package config

// schema_gen.go generates the JSON Schema for config.toml by reflecting over
// fileConfig — the struct that carries the actual TOML keys — so the file can
// never drift from what Load() accepts (P3.1).
//
// The output is byte-stable: fields emit in declaration order, descriptions
// come from the table below, and encoding/json quotes every string. Regenerate
// with `make schema` after changing Config/fileConfig.

import (
	"bytes"
	"encoding/json"
	"reflect"
)

const schemaPath = "../../schema/config.json"

// schemaDescriptions holds one short description per TOML key, sourced from
// the code comments and defaults() — nothing invented.
var schemaDescriptions = map[string]string{
	"data_dir":        "Override the base data directory (default: $XDG_DATA_HOME); cannot relocate its own config directory.",
	"config_dir":      "Override the base configuration directory (default: $XDG_CONFIG_HOME).",
	"poll_interval":   "Daemon poll interval as a Go duration string (default \"10s\").",
	"zoxide_cap":      "Maximum zoxide rows while the query is empty (default 40).",
	"max_show":        "Visible picker rows (default 12).",
	"git_concurrency": "Git enrichment workers (default 4).",
	"proc_cache_ttl":  "Pane process-detection cache TTL as a duration string (default \"2s\").",
	"prune_cutoff":    "Age cutoff for pruning stale rows as a duration string (default \"720h\").",
	"icons":           "TUI glyphs: auto | nerd | ascii (default \"auto\").",
	"autostart":       "Let the picker start gotomuxd when it is not running (default true).",
	"prewarm":         "Page-cache warm on daemon start: auto (rotational disks) | on | off (default \"auto\").",
}

func GenerateFileSchema() []byte {
	var b bytes.Buffer
	b.WriteString("{\n")
	b.WriteString("  \"$schema\": \"https://json-schema.org/draft/2020-12/schema\",\n")
	b.WriteString("  \"title\": \"gotomux config.toml\",\n")
	b.WriteString("  \"type\": \"object\",\n")
	b.WriteString("  \"properties\": {\n")
	writeSchemaProperties(&b, reflect.TypeOf(fileConfig{}), "    ")
	b.WriteString("  },\n")
	b.WriteString("  \"additionalProperties\": false\n")
	b.WriteString("}\n")
	return b.Bytes()
}

// writeSchemaProperties emits each tagged field of t as one property at the
// given indent, comma-separated in declaration order.
func writeSchemaProperties(b *bytes.Buffer, t reflect.Type, indent string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := f.Tag.Get("toml")
		if name == "" || name == "-" {
			continue
		}
		b.WriteString(indent + quoteJSON(name) + ": ")
		writeSchemaProperty(b, f, indent)
		if i < t.NumField()-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
}

// writeSchemaProperty emits one property object: description (when known),
// then either a scalar type or a nested closed object.
func writeSchemaProperty(b *bytes.Buffer, f reflect.StructField, indent string) {
	typ := f.Type
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	desc := schemaDescriptions[f.Tag.Get("toml")]
	inner := indent + "  "

	b.WriteString("{\n")
	if desc != "" {
		b.WriteString(inner + "\"description\": " + quoteJSON(desc) + ",\n")
	}
	if typ.Kind() == reflect.Struct {
		b.WriteString(inner + "\"type\": \"object\",\n")
		b.WriteString(inner + "\"properties\": {\n")
		writeSchemaProperties(b, typ, inner+"  ")
		b.WriteString(inner + "},\n")
		b.WriteString(inner + "\"additionalProperties\": false\n")
	} else {
		b.WriteString(inner + "\"type\": " + quoteJSON(schemaType(typ)) + "\n")
	}
	b.WriteString(indent + "}")
}

// schemaType maps the Go field kinds used by fileConfig to JSON Schema types.
// tomlDuration is a named int64 but only decodes from "10s"-style strings, so
// it is a string here.
func schemaType(t reflect.Type) string {
	if t.Name() == "tomlDuration" {
		return "string"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	default:
		return "string"
	}
}

func quoteJSON(s string) string {
	q, _ := json.Marshal(s)
	return string(q)
}
