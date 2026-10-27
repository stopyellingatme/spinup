//! Form field schemas, conversions, controls, and display values.
use super::{PageData, capitalize};
use serde::Serialize;
use spinup_gen::fields::Field;

#[derive(Clone, Default, Serialize)]
#[serde(rename_all = "PascalCase")]
pub(super) struct PageField {
    pub col: String,
    pub label: String,
    pub zod: String,
    pub from_item: String,
    pub to_body: String,
    pub control: String,
    pub description: String,
    pub cell: String,
    pub value: String,
    pub value_class: String,
    pub labels: String,
    pub options: Vec<PageOption>,
}
#[derive(Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub(super) struct PageOption {
    value: String,
    pub(super) key: String,
    label: String,
}

const CHECK: &str = r#"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="size-4" aria-hidden="true"><path d="M20 6 9 17l-5-5" /></svg>"#;

fn js_string(s: &str) -> String {
    format!("'{}'", s.replace('\\', "\\\\").replace('\'', "\\'"))
}
fn pattern(core: &str, flags: &str, nullable: bool, msg: &str) -> String {
    let re = if nullable {
        format!("^({core})?$")
    } else {
        format!("^{core}$")
    };
    format!("z.string().trim().regex(/{re}/{flags}, {})", js_string(msg))
}

impl PageData {
    pub(super) fn field(&mut self, f: &Field) -> PageField {
        let col = f.column();
        let mut label = capitalize(&f.name.snake().replace('_', " "));
        if !f.r#type.reference.is_empty() {
            label.push_str(" ID");
        }
        let item = format!("item.{col}");
        let values = format!("values.{col}");
        let value = if f.nullable {
            format!("{{{item} ?? '—'}}")
        } else {
            format!("{{{item}}}")
        };
        let mut p = PageField {
            col: col.clone(),
            label: label.clone(),
            from_item: item.clone(),
            to_body: values.clone(),
            cell: value.clone(),
            value,
            ..PageField::default()
        };
        let required = if f.nullable { "" } else { " required" };
        let input = |attrs: &str| {
            format!("<Input {{...props}}{attrs} bind:value={{$values.{col}}}{required} />")
        };
        let text = |p: &mut PageField| {
            if f.nullable {
                p.from_item = format!("{item} ?? ''");
                p.to_body = format!("{values} === '' ? null : {values}");
            }
        };
        match f.r#type.name.as_str() {
            "bool" => {
                if f.nullable {
                    self.control = true;
                    p.zod =
                        "z.enum(['', 'true', 'false'], { error: 'Choose yes, no, or not set.' })"
                            .into();
                    p.from_item = format!("{item} === null ? '' : {item} ? 'true' : 'false'");
                    p.to_body = format!("{values} === '' ? null : {values} === 'true'");
                    p.control = format!(
                        "<select {{...props}} bind:value={{$values.{col}}} class={{[control, 'h-8']}}>\n\t<option value=\"\">Not set</option>\n\t<option value=\"true\">Yes</option>\n\t<option value=\"false\">No</option>\n</select>"
                    );
                    p.cell = format!(
                        "{{#if {item} === null}}—{{:else if {item}}}{CHECK}<span class=\"sr-only\">Yes</span>{{:else}}<span class=\"sr-only\">No</span>{{/if}}"
                    );
                    p.value = format!("{{{item} === null ? '—' : {item} ? 'Yes' : 'No'}}");
                } else {
                    self.checkbox = true;
                    p.zod = "z.boolean()".into();
                    p.control = format!("<Checkbox {{...props}} bind:checked={{$values.{col}}} />");
                    p.cell = format!(
                        "{{#if {item}}}{CHECK}<span class=\"sr-only\">Yes</span>{{:else}}<span class=\"sr-only\">No</span>{{/if}}"
                    );
                    p.value = format!("{{{item} ? 'Yes' : 'No'}}");
                }
            }
            "int" | "bigint" | "ref" | "float" => {
                self.input = true;
                let (zod, step) = if f.r#type.name == "float" {
                    ("z.number({ error: 'Enter a number.' })", "any")
                } else {
                    (
                        "z.number({ error: 'Enter a whole number.' }).int('Enter a whole number.')",
                        "1",
                    )
                };
                p.zod = zod.into();
                if f.nullable {
                    p.zod.push_str(".nullable()");
                }
                p.control = input(&format!(" type=\"number\" step=\"{step}\""));
            }
            "date" => {
                self.input = true;
                self.date = true;
                text(&mut p);
                p.zod = pattern(r"\d{4}-\d{2}-\d{2}", "", f.nullable, "Enter a date.");
                p.control = input(" type=\"date\"");
                p.cell = if f.nullable {
                    format!("{{{item} ? formatDate({item}) : '—'}}")
                } else {
                    format!("{{formatDate({item})}}")
                };
                p.value.clone_from(&p.cell);
            }
            "time" => {
                self.input = true;
                self.time = true;
                p.zod = pattern(
                    r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?",
                    "",
                    f.nullable,
                    "Enter a date and time.",
                );
                p.from_item = format!("localTime({item})");
                p.to_body = format!("time({values}, original?.{col})");
                p.cell = format!("{{formatTime({item})}}");
                if f.nullable {
                    p.from_item = format!("{item} ? localTime({item}) : ''");
                    p.to_body = format!("{values} === '' ? null : time({values}, original?.{col})");
                    p.cell = format!("{{{item} ? formatTime({item}) : '—'}}");
                }
                p.value.clone_from(&p.cell);
                p.control = input(" type=\"datetime-local\" step=\"1\"");
                p.description = "In UTC.".into();
            }
            "json" => {
                self.control = true;
                self.json = true;
                p.zod = "z.string().refine(isObject, 'Enter a JSON object, for example {\"key\": \"value\"}.').default('{}')".into();
                p.from_item = format!("JSON.stringify({item}, null, 2)");
                p.to_body = format!("JSON.parse({values}) as Record<string, unknown>");
                let pre = format!(
                    "<pre class=\"bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs\">{{JSON.stringify({item}, null, 2)}}</pre>"
                );
                p.value = pre.clone();
                if f.nullable {
                    p.zod = "z.string().refine((v) => v.trim() === '' || isObject(v), 'Enter a JSON object, for example {\"key\": \"value\"}.')".into();
                    p.from_item = format!("{item} === null ? '' : JSON.stringify({item}, null, 2)");
                    p.to_body = format!(
                        "{values}.trim() === '' ? null : (JSON.parse({values}) as Record<string, unknown>)"
                    );
                    p.value = format!("{{#if {item} === null}}—{{:else}}{pre}{{/if}}");
                }
                p.cell.clear();
                p.control = format!(
                    "<textarea {{...props}} rows=\"6\" spellcheck=\"false\" bind:value={{$values.{col}}} class={{[control, 'min-h-24 font-mono']}}{required}></textarea>"
                );
            }
            "text" => {
                self.control = true;
                text(&mut p);
                p.zod = "z.string().trim()".into();
                if !f.nullable {
                    p.zod += &format!(".min(1, {})", js_string(&(label.clone() + " is required.")));
                }
                p.cell.clear();
                p.value_class = "whitespace-pre-wrap".into();
                p.control = format!(
                    "<textarea {{...props}} rows=\"4\" bind:value={{$values.{col}}} class={{[control, 'min-h-24']}}{required}></textarea>"
                );
            }
            "enum" => {
                self.control = true;
                p.labels = format!("{}Labels", f.name.camel());
                let mut options = Vec::new();
                if f.nullable {
                    options.push("''".into());
                }
                for v in &f.r#type.r#enum {
                    options.push(js_string(v));
                    let key = if v.starts_with(|c: char| c.is_ascii_digit()) {
                        js_string(v)
                    } else {
                        v.clone()
                    };
                    p.options.push(PageOption {
                        value: v.clone(),
                        key,
                        label: capitalize(&v.replace('_', " ")),
                    });
                }
                p.zod = format!(
                    "z.enum([{}], {{ error: {} }})",
                    options.join(", "),
                    js_string(&format!("Choose the {}.", label.to_lowercase()))
                );
                let badge = format!(
                    "<Badge variant=\"secondary\">{{{}[{item}]}}</Badge>",
                    p.labels
                );
                p.cell = badge.clone();
                p.value = badge.clone();
                p.control = format!(
                    "<select {{...props}} bind:value={{$values.{col}}} class={{[control, 'h-8']}}{required}>"
                );
                if f.nullable {
                    text(&mut p);
                    p.cell = format!("{{#if {item}}}{badge}{{:else}}—{{/if}}");
                    p.value.clone_from(&p.cell);
                    p.control.push_str("\n\t<option value=\"\">None</option>");
                }
                p.control += &format!(
                    "\n\t{{#each Object.entries({}) as [value, label] (value)}}\n\t\t<option {{value}}>{{label}}</option>\n\t{{/each}}\n</select>",
                    p.labels
                );
            }
            "decimal" | "uuid" => {
                self.input = true;
                text(&mut p);
                let (core, flags, msg, attrs) = if f.r#type.name == "decimal" {
                    (
                        r"-?\d+(\.\d+)?",
                        "",
                        "Enter a decimal number, for example 12.50.",
                        " inputmode=\"decimal\"",
                    )
                } else {
                    (
                        "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}",
                        "i",
                        "Enter a UUID, for example 123e4567-e89b-12d3-a456-426614174000.",
                        " autocomplete=\"off\" spellcheck=\"false\"",
                    )
                };
                p.zod = pattern(core, flags, f.nullable, msg);
                p.control = input(attrs);
            }
            _ => {
                self.input = true;
                text(&mut p);
                p.zod = "z.string().trim()".into();
                if !f.nullable {
                    p.zod += &format!(".min(1, {})", js_string(&(label + " is required.")));
                }
                p.control = input("");
            }
        }
        p
    }
}
