//! Field behavior independent of the rendered app.
use super::*;
use spinup_gen::fields;
use spinup_manifest::lock::Lock;

#[test]
fn all_fields_and_nullable_conversions() {
    let lock = Lock::new();
    let fields = fields::parse(&[
        "title:string",
        "body:text?",
        "done:bool",
        "flag:bool?",
        "rank:int",
        "size:bigint?",
        "ratio:float",
        "price:decimal?",
        "at:time",
        "maybe:time?",
        "due:date?",
        "key:uuid",
        "data:json",
        "meta:json?",
        "owner:ref:person",
        "state:enum:draft|in_review",
        "kind:enum:a|b?",
        "tier:enum:1st|2nd",
    ])
    .unwrap();
    let spec = Spec {
        root: Path::new("."),
        lock: &lock,
        name: Name::parse("todo").unwrap(),
        fields,
        owned: false,
        shared: false,
    };
    let d = PageData::new(&spec);
    assert_eq!(d.title_col, "title");
    assert_eq!(d.toggle.as_ref().unwrap().col, "done");
    assert!(d.date && d.time && d.json && d.control && d.input && d.checkbox && d.list_badge);
    for (col, zod, body) in [
        (
            "flag",
            "z.enum(['', 'true', 'false']",
            "values.flag === '' ? null : values.flag === 'true'",
        ),
        (
            "price",
            "regex(/^(-?\\d+(\\.\\d+)?)?$/",
            "values.price === '' ? null : values.price",
        ),
        (
            "maybe",
            "Enter a date and time.",
            "values.maybe === '' ? null : time(values.maybe, original?.maybe)",
        ),
        (
            "meta",
            "v.trim() === '' || isObject(v)",
            "values.meta.trim() === '' ? null : (JSON.parse(values.meta) as Record<string, unknown>)",
        ),
        ("owner_id", "z.number(", "values.owner_id"),
        (
            "kind",
            "z.enum(['', 'a', 'b']",
            "values.kind === '' ? null : values.kind",
        ),
    ] {
        let f = d.fields.iter().find(|f| f.col == col).unwrap();
        assert!(f.zod.contains(zod), "{}", f.zod);
        assert_eq!(f.to_body, body);
    }
    let tier = d.fields.iter().find(|f| f.col == "tier").unwrap();
    assert_eq!(tier.options[0].key, "'1st'");
}
