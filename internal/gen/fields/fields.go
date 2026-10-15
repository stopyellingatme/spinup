package fields

import (
	"fmt"
	"strings"
)

// Name is a normalized singular and plural resource name.
type Name struct {
	Singular string
	Plural   string
}

// Snake returns the singular snake case name.
func (n Name) Snake() string { return n.Singular }

// PluralSnake returns the plural snake case name.
func (n Name) PluralSnake() string { return n.Plural }

// Pascal returns the singular Pascal case name.
func (n Name) Pascal() string { return pascal(n.Singular) }

// PluralPascal returns the plural Pascal case name.
func (n Name) PluralPascal() string { return pascal(n.Plural) }

// Camel returns the singular camel case name.
func (n Name) Camel() string { return camel(n.Singular) }

// PluralCamel returns the plural camel case name.
func (n Name) PluralCamel() string { return camel(n.Plural) }

// Kebab returns the singular kebab case name.
func (n Name) Kebab() string { return strings.ReplaceAll(n.Singular, "_", "-") }

// PluralKebab returns the plural kebab case name.
func (n Name) PluralKebab() string { return strings.ReplaceAll(n.Plural, "_", "-") }

// Table returns the plural snake case table name.
func (n Name) Table() string { return n.Plural }

// ParseName parses a singular or plural resource name.
func ParseName(s string) (Name, error) {
	if s == "" {
		return Name{}, nameError(s, "it is empty")
	}
	if len(s) > 50 {
		return Name{}, nameError(s, "it is longer than 50 characters")
	}
	words, err := nameWords(s)
	if err != nil {
		return Name{}, err
	}
	value := strings.Join(words, "_")
	if value[0] >= '0' && value[0] <= '9' {
		return Name{}, nameError(s, "it starts with a digit")
	}
	singular := singularize(value)
	if reservedNames[singular] {
		return Name{}, nameError(s, fmt.Sprintf("%q is reserved", singular))
	}
	return Name{Singular: singular, Plural: pluralize(singular)}, nil
}

// Type maps a resource field type across generated layers.
type Type struct {
	Name          string
	SQL           string
	GoStore       string
	GoAPI         string
	OpenAPIType   string
	OpenAPIFormat string
	TS            string
	Enum          []string
	Ref           string
}

// Field is a typed resource field.
type Field struct {
	Name     Name
	Type     Type
	Nullable bool
}

// Column returns the field SQL column name.
func (f Field) Column() string {
	if f.Type.Ref != "" {
		return f.Name.Snake() + "_id"
	}
	return f.Name.Snake()
}

var typeTable = []Type{
	{Name: "string", SQL: "text", GoStore: "string", GoAPI: "string", OpenAPIType: "string", TS: "string"},
	{Name: "text", SQL: "text", GoStore: "string", GoAPI: "string", OpenAPIType: "string", TS: "string"},
	{Name: "int", SQL: "integer", GoStore: "int32", GoAPI: "int32", OpenAPIType: "integer", OpenAPIFormat: "int32", TS: "number"},
	{Name: "bigint", SQL: "bigint", GoStore: "int64", GoAPI: "int64", OpenAPIType: "integer", OpenAPIFormat: "int64", TS: "number"},
	{Name: "bool", SQL: "boolean", GoStore: "bool", GoAPI: "bool", OpenAPIType: "boolean", TS: "boolean"},
	{Name: "float", SQL: "double precision", GoStore: "float64", GoAPI: "float64", OpenAPIType: "number", OpenAPIFormat: "double", TS: "number"},
	{Name: "decimal", SQL: "numeric", GoStore: "string", GoAPI: "string", OpenAPIType: "string", OpenAPIFormat: "decimal", TS: "string"},
	{Name: "time", SQL: "timestamptz", GoStore: "time.Time", GoAPI: "time.Time", OpenAPIType: "string", OpenAPIFormat: "date-time", TS: "string"},
	{Name: "date", SQL: "date", GoStore: "time.Time", GoAPI: "openapi_types.Date", OpenAPIType: "string", OpenAPIFormat: "date", TS: "string"},
	{Name: "uuid", SQL: "uuid", GoStore: "uuid.UUID", GoAPI: "uuid.UUID", OpenAPIType: "string", OpenAPIFormat: "uuid", TS: "string"},
	{Name: "json", SQL: "jsonb", GoStore: "[]byte", GoAPI: "map[string]interface{}", OpenAPIType: "object", TS: "Record<string, unknown>"},
	{Name: "ref", SQL: "bigint", GoStore: "int64", GoAPI: "int64", OpenAPIType: "integer", OpenAPIFormat: "int64", TS: "number"},
	{Name: "enum", SQL: "text", GoStore: "string", GoAPI: "string", OpenAPIType: "string", TS: "string"},
}

// Types returns a copy of the type table in a stable order.
func Types() []Type {
	result := make([]Type, len(typeTable))
	copy(result, typeTable)
	return result
}

// Parse parses resource field arguments.
func Parse(args []string) ([]Field, error) {
	fields := make([]Field, 0, len(args))
	seen := make(map[string]struct{}, len(args))
	for _, arg := range args {
		field, err := parseField(arg)
		if err != nil {
			return nil, err
		}
		column := field.Column()
		if column == "id" || column == "created_at" || column == "updated_at" {
			return nil, fmt.Errorf("field %q is automatic\nTry: choose a field other than id, created_at, or updated_at", field.Name.Snake())
		}
		if _, ok := seen[column]; ok {
			return nil, fmt.Errorf("field %q is duplicated\nTry: use each field name once", field.Name.Snake())
		}
		seen[column] = struct{}{}
		fields = append(fields, field)
	}
	return fields, nil
}

func parseField(arg string) (Field, error) {
	nameText, typeText, ok := strings.Cut(arg, ":")
	if !ok || nameText == "" || typeText == "" {
		return Field{}, fmt.Errorf("field %q must use name:type\nTry: use a field such as title:string", arg)
	}
	name, err := ParseName(nameText)
	if err != nil {
		return Field{}, err
	}
	nullable := strings.HasSuffix(typeText, "?")
	if nullable {
		typeText = strings.TrimSuffix(typeText, "?")
	}
	if typeText == "" || strings.Contains(typeText, "?") {
		return Field{}, fmt.Errorf("field %q has an invalid nullable marker\nTry: put one ? after a valid type", nameText)
	}
	typeValue, err := parseType(typeText)
	if err != nil {
		return Field{}, err
	}
	return Field{Name: name, Type: typeValue, Nullable: nullable}, nil
}

func parseType(text string) (Type, error) {
	if target, ok := strings.CutPrefix(text, "ref:"); ok {
		name, err := ParseName(target)
		if err != nil {
			return Type{}, fmt.Errorf("reference type %q is invalid: %w", text, err)
		}
		value := typeByName("ref")
		value.Ref = name.Snake()
		value.SQL = fmt.Sprintf("bigint REFERENCES %s(id)", name.Table())
		return value, nil
	}
	if values, ok := strings.CutPrefix(text, "enum:"); ok {
		return parseEnum(values)
	}
	for _, value := range typeTable[:11] {
		if text == value.Name {
			return value, nil
		}
	}
	return Type{}, fmt.Errorf("field type %q is unknown\nTry: use string, text, int, bigint, bool, float, decimal, time, date, uuid, json, ref:<model>, or enum:a|b", text)
}

func parseEnum(text string) (Type, error) {
	if text == "" {
		return Type{}, fmt.Errorf("enum has no values\nTry: use enum:draft|published")
	}
	values := strings.Split(text, "|")
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validEnumValue(value) {
			return Type{}, fmt.Errorf("enum value %q is invalid\nTry: use lowercase snake case values", value)
		}
		if _, ok := seen[value]; ok {
			return Type{}, fmt.Errorf("enum value %q is duplicated\nTry: use each enum value once", value)
		}
		seen[value] = struct{}{}
	}
	result := typeByName("enum")
	result.Enum = append([]string(nil), values...)
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + value + "'"
	}
	result.TS = strings.Join(quoted, " | ")
	return result, nil
}

func typeByName(name string) Type {
	for _, value := range typeTable {
		if value.Name == name {
			return value
		}
	}
	panic("unknown built-in field type: " + name)
}

func validEnumValue(value string) bool {
	if value == "" || value[0] == '_' || value[len(value)-1] == '_' {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func nameWords(value string) ([]string, error) {
	if strings.Contains(value, "_") || strings.Contains(value, "-") {
		if strings.Contains(value, "_") && strings.Contains(value, "-") {
			return nil, nameError(value, "it mixes snake case and kebab case")
		}
		separator := "_"
		if strings.Contains(value, "-") {
			separator = "-"
		}
		parts := strings.Split(value, separator)
		for _, part := range parts {
			if part == "" || !asciiWord(part) {
				return nil, nameError(value, "it contains an invalid word")
			}
		}
		return lowerWords(parts), nil
	}
	if !asciiWord(value) {
		return nil, nameError(value, "it contains an invalid character")
	}
	return lowerWords(splitCamel(value)), nil
}

func asciiWord(value string) bool {
	for _, r := range value {
		if !isASCIIAlpha(r) && !isASCIIDigit(r) {
			return false
		}
	}
	return true
}

func splitCamel(value string) []string {
	var words []string
	start := 0
	runes := []rune(value)
	for i := 1; i < len(runes); i++ {
		if isASCIIUpper(runes[i]) && (isASCIILower(runes[i-1]) || isASCIIDigit(runes[i-1]) || (i+1 < len(runes) && isASCIILower(runes[i+1]))) {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	return append(words, string(runes[start:]))
}

func isASCIIAlpha(value rune) bool { return isASCIILower(value) || isASCIIUpper(value) }

func isASCIILower(value rune) bool { return value >= 'a' && value <= 'z' }

func isASCIIUpper(value rune) bool { return value >= 'A' && value <= 'Z' }

func isASCIIDigit(value rune) bool { return value >= '0' && value <= '9' }

func lowerWords(words []string) []string {
	for i := range words {
		words[i] = strings.ToLower(words[i])
	}
	return words
}

func pascal(value string) string {
	words := strings.Split(value, "_")
	for i := range words {
		words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
	}
	return strings.Join(words, "")
}

func camel(value string) string {
	p := pascal(value)
	return strings.ToLower(p[:1]) + p[1:]
}

func nameError(value, reason string) error {
	return fmt.Errorf("name %q is invalid because %s\nTry: use a short lowercase resource name", value, reason)
}

var irregularSingular = map[string]string{
	"person": "people", "child": "children", "man": "men", "woman": "women", "mouse": "mice", "goose": "geese", "tooth": "teeth", "foot": "feet",
}

var irregularPlural = map[string]string{
	"people": "person", "children": "child", "men": "man", "women": "woman", "mice": "mouse", "geese": "goose", "teeth": "tooth", "feet": "foot",
}

func pluralize(value string) string {
	if plural, ok := irregularSingular[value]; ok {
		return plural
	}
	if strings.HasSuffix(value, "y") && len(value) > 1 && !isVowel(value[len(value)-2]) {
		return value[:len(value)-1] + "ies"
	}
	if strings.HasSuffix(value, "s") || strings.HasSuffix(value, "x") || strings.HasSuffix(value, "z") || strings.HasSuffix(value, "ch") || strings.HasSuffix(value, "sh") {
		return value + "es"
	}
	return value + "s"
}

func singularize(value string) string {
	if singular, ok := irregularPlural[value]; ok {
		return singular
	}
	if strings.HasSuffix(value, "ies") && len(value) > 3 {
		return value[:len(value)-3] + "y"
	}
	if strings.HasSuffix(value, "ses") || strings.HasSuffix(value, "xes") || strings.HasSuffix(value, "zes") || strings.HasSuffix(value, "ches") || strings.HasSuffix(value, "shes") {
		return value[:len(value)-2]
	}
	if strings.HasSuffix(value, "s") && !strings.HasSuffix(value, "ss") && !strings.HasSuffix(value, "us") && !strings.HasSuffix(value, "is") {
		return value[:len(value)-1]
	}
	return value
}

func isVowel(value byte) bool {
	return value == 'a' || value == 'e' || value == 'i' || value == 'o' || value == 'u'
}

var reservedNames = makeReservedNames()

func makeReservedNames() map[string]bool {
	words := strings.Fields(`
		break default func interface select case defer go map struct chan else goto package switch
		const fallthrough if range type continue for import return var
		any bool byte comparable complex64 complex128 error float32 float64 int int8 int16 int32 int64
		rune string uint uint8 uint16 uint32 uint64 uintptr true false iota nil
		all analyse analyze and any array as asc asymmetric authorization between binary both case cast check collate column concurrently constraint create cross current_catalog current_date current_role current_time current_timestamp current_user default deferrable desc distinct do else end except false fetch for foreign from grant group having ilike in initially inner intersect into is join lateral leading left like limit localtime localtimestamp natural not notnull null offset on only or order outer overlaps placing primary references returning right select session_user similar some symmetric table then to trailing true union unique user using variadic verbose when where window with
	`)
	result := make(map[string]bool, len(words))
	for _, word := range words {
		result[word] = true
	}
	return result
}
