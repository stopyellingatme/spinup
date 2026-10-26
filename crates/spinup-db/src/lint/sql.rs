use std::collections::BTreeSet;

pub(super) struct Statement {
    pub tokens: Vec<String>,
    pub line: usize,
}

fn identifier(token: &str) -> String {
    token
        .rsplit('.')
        .next()
        .unwrap_or(token)
        .trim_matches('"')
        .to_lowercase()
}

pub(super) fn create_table(tokens: &[String]) -> Option<String> {
    if tokens.first()?.as_str() != "CREATE" {
        return None;
    }
    let mut i = 1;
    while tokens.get(i).is_some_and(|s| {
        matches!(
            s.as_str(),
            "UNLOGGED" | "TEMP" | "TEMPORARY" | "GLOBAL" | "LOCAL"
        )
    }) {
        i += 1;
    }
    if tokens.get(i)?.as_str() != "TABLE" {
        return None;
    }
    i += 1;
    if sequence_at(tokens, i, &["IF", "NOT", "EXISTS"]) {
        i += 3;
    }
    tokens.get(i).map(|s| identifier(s))
}

fn sequence_at(tokens: &[String], i: usize, sequence: &[&str]) -> bool {
    tokens.get(i..i + sequence.len()).is_some_and(|slice| {
        slice
            .iter()
            .map(String::as_str)
            .eq(sequence.iter().copied())
    })
}

fn has(tokens: &[String], word: &str) -> bool {
    tokens.iter().any(|s| s == word)
}

fn clauses(tokens: &[String]) -> Vec<&[String]> {
    let mut i = 2;
    if sequence_at(tokens, i, &["IF", "EXISTS"]) {
        i += 2;
    }
    if tokens.get(i).is_some_and(|s| s == "ONLY") {
        i += 1;
    }
    let mut start = i + 1;
    let mut depth: usize = 0;
    let mut result = Vec::new();
    for (i, token) in tokens.iter().enumerate().skip(start) {
        match token.as_str() {
            "(" => depth += 1,
            ")" => depth = depth.saturating_sub(1),
            "," if depth == 0 => {
                result.push(&tokens[start..i]);
                start = i + 1;
            }
            _ => {}
        }
    }
    if let Some(tail) = tokens.get(start..) {
        result.push(tail);
    }
    result
}

type Rule = (&'static str, &'static str, &'static str);

pub(super) fn rule_for(
    tokens: &[String],
    earlier: Option<&BTreeSet<String>>,
    created: &BTreeSet<String>,
    no_transaction: bool,
) -> Option<Rule> {
    if sequence_at(tokens, 0, &["DROP", "TABLE"]) {
        return Some((
            "drop-table",
            "dropping a table breaks old application versions",
            "remove the table in a later release after all old versions stop using it",
        ));
    }
    if tokens.first().is_some_and(|s| s == "DO" || s == "EXECUTE") {
        return Some((
            "unchecked-dynamic-sql",
            "dynamic SQL can hide unsafe schema changes",
            "avoid dynamic DDL, or add -- spinup:allow-destructive with a reason",
        ));
    }
    if sequence_at(tokens, 0, &["ALTER", "TABLE"]) && tokens.len() > 3 {
        for clause in clauses(tokens) {
            let Some(first) = clause.first() else {
                continue;
            };
            let mut i = 1;
            if clause.get(i).is_some_and(|s| s == "COLUMN") {
                i += 1;
            }
            match first.as_str() {
                "DROP" => {
                    if sequence_at(clause, i, &["IF", "EXISTS"]) {
                        i += 2;
                    }
                    if clause
                        .get(i)
                        .is_some_and(|s| !matches!(s.as_str(), "CONSTRAINT" | "DEFAULT" | "NOT"))
                    {
                        return Some((
                            "drop-column",
                            "dropping a column breaks old application versions",
                            "remove the column in a later release after all old versions stop using it",
                        ));
                    }
                }
                "RENAME" => {
                    return Some((
                        "rename",
                        "renaming database objects breaks old application versions",
                        "add a compatible replacement and remove the old name in a later release",
                    ));
                }
                "ALTER" => {
                    if sequence_at(clause, i + 1, &["TYPE"])
                        || sequence_at(clause, i + 1, &["SET", "DATA"])
                    {
                        return Some((
                            "alter-type",
                            "changing a column type can break old application versions",
                            "add a new compatible column and migrate data in a later release",
                        ));
                    }
                    if sequence_at(clause, i + 1, &["SET", "NOT"]) {
                        return Some((
                            "set-not-null",
                            "adding NOT NULL can reject writes from old application versions",
                            "backfill first, then add the constraint in a later release",
                        ));
                    }
                }
                "ADD" => {
                    if clause.get(i).is_none_or(|s| {
                        matches!(
                            s.as_str(),
                            "CONSTRAINT" | "PRIMARY" | "UNIQUE" | "CHECK" | "FOREIGN"
                        )
                    }) {
                        continue;
                    }
                    let tail = &clause[i..];
                    if tail.windows(2).any(|w| w == ["NOT", "NULL"])
                        && !has(tail, "DEFAULT")
                        && (!has(tail, "GENERATED") || !has(tail, "STORED"))
                    {
                        return Some((
                            "set-not-null",
                            "adding a required column without a default breaks old application versions",
                            "add the column as nullable or with a default, then constrain it in a later release",
                        ));
                    }
                }
                _ => {}
            }
        }
    }
    if sequence_at(tokens, 0, &["CREATE", "INDEX"])
        || sequence_at(tokens, 0, &["CREATE", "UNIQUE", "INDEX"])
    {
        if has(tokens, "CONCURRENTLY") {
            return (!no_transaction).then_some((
                "concurrent-in-tx",
                "CREATE INDEX CONCURRENTLY cannot run in a transaction",
                "add -- no-transaction to this migration",
            ));
        }
        if let Some(i) = tokens.iter().position(|s| s == "ON") {
            let mut i = i + 1;
            if tokens.get(i).is_some_and(|s| s == "ONLY") {
                i += 1;
            }
            if let Some(table) = tokens.get(i).map(|s| identifier(s))
                && !created.contains(&table)
                && earlier.is_none_or(|e| e.contains(&table))
            {
                return Some((
                    "index-not-concurrent",
                    "creating an index on an existing table can block writes",
                    "use CREATE INDEX CONCURRENTLY in a no-transaction migration",
                ));
            }
        }
    }
    None
}

fn word(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_'
}

fn quoted(bytes: &[u8], mut i: usize, quote: u8) -> usize {
    i += 1;
    while i < bytes.len() {
        if bytes[i] == quote {
            if bytes.get(i + 1) == Some(&quote) {
                i += 2;
            } else {
                return i + 1;
            }
        } else {
            i += 1;
        }
    }
    i
}

fn identifier_end(bytes: &[u8], mut i: usize) -> usize {
    loop {
        if bytes.get(i) == Some(&b'"') {
            i = quoted(bytes, i, b'"');
        } else {
            while i < bytes.len() && word(bytes[i]) {
                i += 1;
            }
        }
        if bytes.get(i) != Some(&b'.') {
            return i;
        }
        i += 1;
    }
}

pub(super) fn tokenize(sql: &str) -> Vec<Statement> {
    let bytes = sql.as_bytes();
    let mut statements = Vec::new();
    let mut tokens = Vec::new();
    let (mut i, mut line, mut start) = (0, 1, 1);
    while i < bytes.len() {
        let from = i;
        match bytes[i] {
            b'\n' => {
                line += 1;
                i += 1;
                continue;
            }
            c if c.is_ascii_whitespace() => {
                i += 1;
                continue;
            }
            b'-' if bytes.get(i + 1) == Some(&b'-') => {
                while i < bytes.len() && bytes[i] != b'\n' {
                    i += 1;
                }
                continue;
            }
            b'/' if bytes.get(i + 1) == Some(&b'*') => {
                i += 2;
                let mut depth = 1;
                while i < bytes.len() && depth > 0 {
                    if bytes.get(i..i + 2) == Some(b"/*") {
                        depth += 1;
                        i += 2;
                    } else if bytes.get(i..i + 2) == Some(b"*/") {
                        depth -= 1;
                        i += 2;
                    } else {
                        if bytes[i] == b'\n' {
                            line += 1;
                        }
                        i += 1;
                    }
                }
                continue;
            }
            b'\'' => {
                i = quoted(bytes, i, b'\'');
                line += bytes[from..i].iter().filter(|c| **c == b'\n').count();
                continue;
            }
            b'$' => {
                let mut end = i + 1;
                while end < bytes.len() && word(bytes[end]) {
                    end += 1;
                }
                if bytes.get(end) == Some(&b'$') {
                    let tag = &sql[i..=end];
                    if let Some(close) = sql[end + 1..].find(tag) {
                        i = end + 1 + close + tag.len();
                        line += bytes[from..i].iter().filter(|c| **c == b'\n').count();
                        continue;
                    }
                }
                i += 1;
                continue;
            }
            b';' => {
                if !tokens.is_empty() {
                    statements.push(Statement {
                        tokens: std::mem::take(&mut tokens),
                        line: start,
                    });
                }
                i += 1;
                continue;
            }
            b'(' | b')' | b',' => i += 1,
            b'"' => i = identifier_end(bytes, i),
            c if word(c) => i = identifier_end(bytes, i),
            _ => {
                i += 1;
                continue;
            }
        }
        if tokens.is_empty() {
            start = line;
        }
        // All token boundaries are ASCII, except complete quoted identifiers.
        tokens.push(sql[from..i].to_uppercase());
        line += bytes[from..i].iter().filter(|c| **c == b'\n').count();
    }
    if !tokens.is_empty() {
        statements.push(Statement {
            tokens,
            line: start,
        });
    }
    statements
}
