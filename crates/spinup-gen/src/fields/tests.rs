use super::*;

#[test]
fn names_and_inflection() {
    for (input, singular, plural) in [
        ("blog_post", "blog_post", "blog_posts"),
        ("blog-posts", "blog_post", "blog_posts"),
        ("blogPost", "blog_post", "blog_posts"),
        ("BlogPosts", "blog_post", "blog_posts"),
        ("people", "person", "people"),
        ("children", "child", "children"),
        ("categories", "category", "categories"),
        ("statuses", "status", "statuses"),
        ("addresses", "address", "addresses"),
        ("cases", "case", "cases"),
        ("responses", "response", "responses"),
        ("movies", "movie", "movies"),
        ("cookies", "cookie", "cookies"),
        ("series", "series", "series"),
        ("news", "news", "news"),
        ("species", "species", "species"),
        ("ties", "tie", "ties"),
        ("users", "user", "users"),
        ("orders", "order", "orders"),
        ("HTTPServer", "http_server", "http_servers"),
    ] {
        let name = parse_name(input).unwrap();
        assert_eq!((name.snake(), name.plural_snake()), (singular, plural));
        assert_eq!(name.table(), plural);
    }
    let name = parse_name("BlogPosts").unwrap();
    assert_eq!(
        (name.pascal(), name.plural_pascal()),
        ("BlogPost".into(), "BlogPosts".into())
    );
    assert_eq!(
        (name.camel(), name.plural_camel()),
        ("blogPost".into(), "blogPosts".into())
    );
    assert_eq!(
        (name.kebab(), name.plural_kebab()),
        ("blog-post".into(), "blog-posts".into())
    );
    for input in [
        "",
        "9lives",
        "mötley",
        "foo_bar-baz",
        "foo__bar",
        "foo bar",
        "s",
    ] {
        assert!(
            parse_name(input)
                .unwrap_err()
                .to_string()
                .contains("\nTry:"),
            "{input}"
        );
    }
    assert!(parse_name(&"a".repeat(51)).is_err());
}

#[test]
fn types_and_nullable_fields() {
    let expected = [
        ("string", "text", "String"),
        ("text", "text", "String"),
        ("int", "integer", "i32"),
        ("bigint", "bigint", "i64"),
        ("bool", "boolean", "bool"),
        ("float", "double precision", "f64"),
        ("decimal", "numeric", "String"),
        ("time", "timestamptz", "time::OffsetDateTime"),
        ("date", "date", "time::Date"),
        ("uuid", "uuid", "uuid::Uuid"),
        ("json", "jsonb", "serde_json::Value"),
        ("ref", "bigint", "i64"),
        ("enum", "text", "String"),
    ];
    let table = types();
    assert_eq!(table.len(), expected.len());
    for (ty, (name, sql, rust)) in table.iter().zip(expected) {
        assert_eq!(
            (&*ty.name, &*ty.sql, &*ty.rust_store, &*ty.rust_api),
            (name, sql, rust, rust)
        );
        let text = match name {
            "ref" => "ref:person",
            "enum" => "enum:draft|published",
            _ => name,
        };
        for suffix in ["", "?"] {
            let fields = parse(&[format!("value:{text}{suffix}")]).unwrap();
            assert_eq!(fields[0].nullable, suffix == "?");
            let rust_type = if suffix == "?" {
                format!("Option<{rust}>")
            } else {
                rust.into()
            };
            assert_eq!(fields[0].rust_type(), rust_type);
        }
    }
    let fields = parse(&[
        "price:decimal",
        "author:ref:people",
        "owner:ref:orders",
        "state:enum:draft|in_review|published",
    ])
    .unwrap();
    assert_eq!(fields[0].bind("$1"), "$1::text::numeric");
    assert_eq!(fields[0].select(), "price::text");
    assert_eq!(fields[1].column(), "author_id");
    assert_eq!(fields[1].r#type.reference, "person");
    assert_eq!(fields[1].r#type.sql, "bigint REFERENCES people(id)");
    assert_eq!(fields[2].r#type.sql, "bigint REFERENCES orders(id)");
    assert_eq!(fields[3].r#type.r#enum, ["draft", "in_review", "published"]);
    assert_eq!(fields[3].r#type.ts, "'draft' | 'in_review' | 'published'");
}

#[test]
fn fields_keep_plural_names_and_reject_invalid_inputs() {
    let fields = parse(&["tags:json", "s:string", "dueDate:date", "status:string"]).unwrap();
    assert_eq!(
        fields.iter().map(Field::column).collect::<Vec<_>>(),
        ["tags", "s", "due_date", "status"]
    );
    for args in [
        vec!["id:string"],
        vec!["created_at:time"],
        vec!["updated_at:time"],
        vec!["title:string", "Title:text"],
        vec!["author:ref:user", "author_id:bigint"],
        vec!["title:unknown"],
        vec!["order:string"],
        vec!["limit:int"],
        vec!["end:date"],
        vec!["type:string"],
        vec!["async:string"],
        vec!["self:string"],
        vec!["gen:string"],
        vec!["state:enum:Draft|published"],
        vec!["state:enum:draft|draft"],
        vec!["state:enum:"],
        vec!["state:enum:_draft"],
        vec!["state:enum:draft_"],
        vec!["author:ref:"],
        vec!["title:string??"],
        vec!["title:?"],
        vec!["invalid"],
        vec![":string"],
    ] {
        assert!(
            parse(&args).unwrap_err().to_string().contains("\nTry:"),
            "{args:?}"
        );
    }
}
