package resource

import (
	"fmt"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen/fields"
)

// pageField is one field of the pages.
type pageField struct {
	// Col is the JSON property and the form input name.
	Col string
	// Label is the visible name, such as "Owner ID".
	Label string
	// Zod is the zod schema of the form value.
	Zod string
	// FromItem converts item.<Col> to the form value.
	FromItem string
	// ToBody converts values.<Col> to the request property.
	ToBody string
	// Control is the markup of the input. It spreads the props of the field
	// component.
	Control string
	// Description is the hint below the input, or empty.
	Description string
	// Cell is the list cell markup, or empty when the list does not show the
	// field.
	Cell string
	// Value is the markup of the value in the read view.
	Value string
	// ValueClass is the class of the dd element of the value, or empty.
	ValueClass string
	// Labels names the map of enum labels, such as priorityLabels. It is
	// empty for other types.
	Labels string
	// Options are the enum values and their labels.
	Options []pageOption
}

// pageOption is one enum value and its label. Key is the value as an object
// key: quoted when it is not an identifier, such as '1st'.
type pageOption struct{ Value, Key, Label string }

// The patterns of the form values that are text inputs with a format. A
// nullable field also accepts an empty value.
const (
	datePattern    = `\d{4}-\d{2}-\d{2}`
	timePattern    = `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?`
	uuidPattern    = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	decimalPattern = `-?\d+(\.\d+)?`
)

// pattern returns a zod string schema that accepts core, or also an empty
// value when nullable is set.
func pattern(core, flags string, nullable bool, msg string) string {
	re := "^" + core + "$"
	if nullable {
		re = "^(" + core + ")?$"
	}
	return fmt.Sprintf("z.string().trim().regex(/%s/%s, %s)", re, flags, jsString(msg))
}

// jsString quotes s as a TypeScript string with single quotes.
func jsString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// orDash shows a dash for a null value.
func orDash(expr string) string { return fmt.Sprintf("{%s ?? '—'}", expr) }

// field maps one field to its zod schema, its conversions, its input, its
// list cell, and its read view value. The inputs follow the field table: a
// text input, a textarea for text and json, a checkbox for a bool, a select
// for a nullable bool and an enum, a number input for numbers and refs, and
// date and datetime-local inputs.
func (d *pageData) field(f fields.Field) pageField {
	col := f.Column()
	label := capitalize(words(f.Name.Snake()))
	if f.Type.Ref != "" {
		label += " ID"
	}
	item, values := "item."+col, "values."+col
	p := pageField{
		Col:      col,
		Label:    label,
		FromItem: item,
		ToBody:   values,
		Cell:     "{" + item + "}",
		Value:    "{" + item + "}",
	}
	required := " required"
	if f.Nullable {
		required = ""
		p.Cell, p.Value = orDash(item), orDash(item)
	}
	// text converts between a nullable string column and a string form value.
	text := func() {
		if f.Nullable {
			p.FromItem = item + " ?? ''"
			p.ToBody = fmt.Sprintf("%s === '' ? null : %s", values, values)
		}
	}
	input := func(attrs string) string {
		return fmt.Sprintf("<Input {...props}%s bind:value={$values.%s}%s />", attrs, col, required)
	}
	switch f.Type.Name {
	case "bool":
		if f.Nullable {
			d.Control = true
			p.Zod = "z.enum(['', 'true', 'false'], { error: 'Choose yes, no, or not set.' })"
			p.FromItem = fmt.Sprintf("%s === null ? '' : %s ? 'true' : 'false'", item, item)
			p.ToBody = fmt.Sprintf("%s === '' ? null : %s === 'true'", values, values)
			p.Control = fmt.Sprintf(`<select {...props} bind:value={$values.%s} class={[control, 'h-8']}>
	<option value="">Not set</option>
	<option value="true">Yes</option>
	<option value="false">No</option>
</select>`, col)
			p.Cell = fmt.Sprintf(`{#if %s === null}—{:else if %s}`+checkSVG+`<span class="sr-only">Yes</span>{:else}<span class="sr-only">No</span>{/if}`, item, item)
			p.Value = fmt.Sprintf("{%s === null ? '—' : %s ? 'Yes' : 'No'}", item, item)
			break
		}
		d.Checkbox = true
		p.Zod = "z.boolean()"
		p.Control = fmt.Sprintf("<Checkbox {...props} bind:checked={$values.%s} />", col)
		p.Cell = fmt.Sprintf(`{#if %s}`+checkSVG+`<span class="sr-only">Yes</span>{:else}<span class="sr-only">No</span>{/if}`, item)
		p.Value = fmt.Sprintf("{%s ? 'Yes' : 'No'}", item)
	case "int", "bigint", "ref", "float":
		d.Input = true
		zod, step := "z.number({ error: 'Enter a whole number.' }).int('Enter a whole number.')", "1"
		if f.Type.Name == "float" {
			zod, step = "z.number({ error: 'Enter a number.' })", "any"
		}
		if f.Nullable {
			zod += ".nullable()"
		}
		p.Zod = zod
		p.Control = input(fmt.Sprintf(` type="number" step="%s"`, step))
	case "date":
		d.Input, d.Date = true, true
		text()
		p.Zod = pattern(datePattern, "", f.Nullable, "Enter a date.")
		p.Control = input(` type="date"`)
		p.Cell = fmt.Sprintf("{formatDate(%s)}", item)
		if f.Nullable {
			p.Cell = fmt.Sprintf("{%s ? formatDate(%s) : '—'}", item, item)
		}
		p.Value = p.Cell
	case "time":
		d.Input, d.Time = true, true
		p.Zod = pattern(timePattern, "", f.Nullable, "Enter a date and time.")
		p.FromItem = fmt.Sprintf("localTime(%s)", item)
		p.ToBody = fmt.Sprintf("time(%s, original?.%s)", values, col)
		p.Cell = fmt.Sprintf("{formatTime(%s)}", item)
		if f.Nullable {
			p.FromItem = fmt.Sprintf("%s ? localTime(%s) : ''", item, item)
			p.ToBody = fmt.Sprintf("%s === '' ? null : time(%s, original?.%s)", values, values, col)
			p.Cell = fmt.Sprintf("{%s ? formatTime(%s) : '—'}", item, item)
		}
		p.Value = p.Cell
		p.Control = input(` type="datetime-local" step="1"`)
		p.Description = "In UTC."
	case "json":
		d.Control, d.JSON = true, true
		p.Zod = "z.string().refine(isObject, 'Enter a JSON object, for example {\"key\": \"value\"}.').default('{}')"
		p.FromItem = fmt.Sprintf("JSON.stringify(%s, null, 2)", item)
		p.ToBody = fmt.Sprintf("JSON.parse(%s) as Record<string, unknown>", values)
		pre := fmt.Sprintf(`<pre class="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs">{JSON.stringify(%s, null, 2)}</pre>`, item)
		p.Value = pre
		if f.Nullable {
			p.Zod = "z.string().refine((v) => v.trim() === '' || isObject(v), 'Enter a JSON object, for example {\"key\": \"value\"}.')"
			p.FromItem = fmt.Sprintf("%s === null ? '' : JSON.stringify(%s, null, 2)", item, item)
			p.ToBody = fmt.Sprintf("%s.trim() === '' ? null : (JSON.parse(%s) as Record<string, unknown>)", values, values)
			p.Value = fmt.Sprintf("{#if %s === null}—{:else}%s{/if}", item, pre)
		}
		p.Cell = ""
		p.Control = fmt.Sprintf(`<textarea {...props} rows="6" spellcheck="false" bind:value={$values.%s} class={[control, 'min-h-24 font-mono']}%s></textarea>`, col, required)
	case "text":
		d.Control = true
		text()
		p.Zod = "z.string().trim()"
		if !f.Nullable {
			p.Zod += fmt.Sprintf(".min(1, %s)", jsString(label+" is required."))
		}
		p.Cell = ""
		p.ValueClass = "whitespace-pre-wrap"
		p.Control = fmt.Sprintf(`<textarea {...props} rows="4" bind:value={$values.%s} class={[control, 'min-h-24']}%s></textarea>`, col, required)
	case "enum":
		d.Control = true
		p.Labels = f.Name.Camel() + "Labels"
		values := make([]string, 0, len(f.Type.Enum)+1)
		if f.Nullable {
			values = append(values, "''")
		}
		for _, v := range f.Type.Enum {
			values = append(values, jsString(v))
			key := v
			if v[0] >= '0' && v[0] <= '9' {
				key = jsString(v)
			}
			p.Options = append(p.Options, pageOption{Value: v, Key: key, Label: capitalize(words(v))})
		}
		p.Zod = fmt.Sprintf("z.enum([%s], { error: %s })", strings.Join(values, ", "), jsString("Choose the "+strings.ToLower(label)+"."))
		badge := fmt.Sprintf(`<Badge variant="secondary">{%s[%s]}</Badge>`, p.Labels, item)
		p.Cell, p.Value = badge, badge
		var b strings.Builder
		fmt.Fprintf(&b, "<select {...props} bind:value={$values.%s} class={[control, 'h-8']}%s>", col, required)
		if f.Nullable {
			p.FromItem = item + " ?? ''"
			p.ToBody = fmt.Sprintf("%s === '' ? null : %s", "values."+col, "values."+col)
			p.Cell = fmt.Sprintf("{#if %s}%s{:else}—{/if}", item, badge)
			p.Value = p.Cell
			b.WriteString("\n\t<option value=\"\">None</option>")
		}
		fmt.Fprintf(&b, "\n\t{#each Object.entries(%s) as [value, label] (value)}\n\t\t<option {value}>{label}</option>\n\t{/each}\n</select>", p.Labels)
		p.Control = b.String()
	case "decimal":
		d.Input = true
		text()
		p.Zod = pattern(decimalPattern, "", f.Nullable, "Enter a decimal number, for example 12.50.")
		p.Control = input(` inputmode="decimal"`)
	case "uuid":
		d.Input = true
		text()
		p.Zod = pattern(uuidPattern, "i", f.Nullable, "Enter a UUID, for example 123e4567-e89b-12d3-a456-426614174000.")
		p.Control = input(` autocomplete="off" spellcheck="false"`)
	default: // string
		d.Input = true
		text()
		p.Zod = "z.string().trim()"
		if !f.Nullable {
			p.Zod += fmt.Sprintf(".min(1, %s)", jsString(label+" is required."))
		}
		p.Control = input("")
	}
	return p
}

// checkSVG is the check mark of a list cell for a bool. It is the Lucide check icon as inline markup.
// A list has many rows, and an inline svg is cheaper to render than an icon component.
const checkSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="size-4" aria-hidden="true"><path d="M20 6 9 17l-5-5" /></svg>`
