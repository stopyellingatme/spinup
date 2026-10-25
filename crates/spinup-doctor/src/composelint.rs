//! The Compose file lint (Go `internal/doctor/composelint`).
//!
//! The lint reads a Compose file and reports unsafe service settings. The `compose-security`
//! check runs it on every `deploy/compose*.yml` and `deploy/compose*.yaml` file.
//!
//! The YAML model of `spinup-editors` keeps no comments. The lint reads the suppression
//! comments (`spinup:allow <rule> <reason>`) from the source text. A comment on a service
//! key, or on a direct child key of a service, applies to the whole service. A comment on a
//! deeper line (a list item) applies to its rule in the whole service too. Go applies it to
//! that item only.

use std::collections::HashMap;
use std::net::IpAddr;
use std::sync::LazyLock;

use linkme::distributed_slice;
use regex::Regex;
use spinup_editors::yaml::{Kind, Node, Resolved, Style, Tag, resolve};

use crate::registry::{CHECKS, Check, Context, Outcome, Scope, Status};

static INTERPOLATION: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^(?:\$[A-Za-z_][A-Za-z0-9_]*|\$\{[^}]+\})$").unwrap());
static SECRET_NAME: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)(PASSWORD|SECRET|TOKEN|API_KEY|PRIVATE_KEY)").unwrap());
static SUPPRESSION: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?m)spinup:allow\s+(\S+)(?:\s+([^\r\n]+?))?\s*$").unwrap());
static SECRET_DEFAULT: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\$\{[A-Za-z_][A-Za-z0-9_]*(?::-|-)([^}]*)\}").unwrap());
static URL_CREDENTIALS: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"[A-Za-z][A-Za-z0-9+.-]*://([^/\s@]+)@").unwrap());
static BLOCK_SCALAR: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?:^|[\s:-])[|>][+-]?[0-9]*[+-]?$").unwrap());

/// One Compose security problem.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Finding {
    /// The file path, as given to [`lint`].
    pub file: String,
    /// The service name.
    pub service: String,
    /// The rule name.
    pub rule: String,
    /// The message for the user.
    pub message: String,
    /// The hint that follows `Try:`.
    pub hint: String,
}

/// The error for Compose data that is not valid YAML.
#[derive(Debug, thiserror::Error)]
#[error("{0}")]
pub struct LintError(String);

/// Checks Compose YAML data for unsafe service settings.
pub fn lint(file: &str, data: &str) -> Result<Vec<Finding>, LintError> {
    let document = Node::parse(data).map_err(|e| LintError(e.reason().to_string()))?;
    let mut anchors = HashMap::new();
    let Some(root) = resolve_node(&document, &mut anchors, &mut Vec::new()) else {
        return Ok(Vec::new());
    };
    let Some(services) = mapping_value(&root, "services") else {
        return Ok(Vec::new());
    };
    let Kind::Map(entries) = &services.kind else {
        return Ok(Vec::new());
    };
    let mut findings = Vec::new();
    for (key, service) in entries {
        if !matches!(service.kind, Kind::Map(_)) {
            continue;
        }
        let name = key.as_str().unwrap_or("");
        let comments = service_comments(data, name);
        findings.extend(lint_service(file, name, service, &comments));
    }
    findings
        .sort_by(|a, b| (&a.service, &a.rule, &a.message).cmp(&(&b.service, &b.rule, &b.message)));
    Ok(findings)
}

/// Copies aliases and YAML merges without changing the parsed tree.
/// A merge sequence prefers its first mapping. Explicit keys always win.
fn resolve_node(
    node: &Node,
    anchors: &mut HashMap<String, Node>,
    active: &mut Vec<String>,
) -> Option<Node> {
    if !node.anchor.is_empty() {
        anchors.insert(node.anchor.clone(), node.clone());
    }
    match &node.kind {
        Kind::Alias(name) => {
            if active.contains(name) {
                return None;
            }
            let target = anchors.get(name)?.clone();
            active.push(name.clone());
            let resolved = resolve_node(&target, anchors, active);
            active.pop();
            resolved
        }
        Kind::Scalar(_) => Some(node.clone()),
        Kind::Seq(items) => {
            let items = items
                .iter()
                .filter_map(|item| resolve_node(item, anchors, active))
                .collect();
            Some(Node {
                kind: Kind::Seq(items),
                ..node.clone()
            })
        }
        Kind::Map(entries) => {
            let mut out: Vec<(Node, Node)> = Vec::new();
            let mut seen: Vec<String> = Vec::new();
            let mut merges: Vec<&Node> = Vec::new();
            for (key, value) in entries {
                if is_merge_key(key) {
                    merges.push(value);
                    continue;
                }
                if let Some(resolved) = resolve_node(value, anchors, active) {
                    seen.push(key.as_str().unwrap_or("").to_string());
                    out.push((key.clone(), resolved));
                }
            }
            for merge in merges {
                let Some(resolved) = resolve_node(merge, anchors, active) else {
                    continue;
                };
                for source in sequence_values(&resolved) {
                    let Kind::Map(source_entries) = &source.kind else {
                        continue;
                    };
                    for (key, value) in source_entries {
                        let name = key.as_str().unwrap_or("");
                        if !seen.iter().any(|s| s == name) {
                            seen.push(name.to_string());
                            out.push((key.clone(), value.clone()));
                        }
                    }
                }
            }
            Some(Node {
                kind: Kind::Map(out),
                ..node.clone()
            })
        }
    }
}

fn is_merge_key(key: &Node) -> bool {
    key.as_str() == Some("<<") && key.tag == Tag::Implicit && key.style == Style::Any
}

fn mapping_value<'a>(node: &'a Node, key: &str) -> Option<&'a Node> {
    let Kind::Map(entries) = &node.kind else {
        return None;
    };
    entries
        .iter()
        .find(|(k, _)| k.as_str() == Some(key))
        .map(|(_, v)| v)
}

/// Returns the items of a sequence, or the node itself for any other node.
fn sequence_values(node: &Node) -> Vec<&Node> {
    match &node.kind {
        Kind::Seq(items) => items.iter().collect(),
        _ => vec![node],
    }
}

fn text(node: &Node) -> &str {
    node.as_str().unwrap_or("")
}

fn is_true(node: &Node) -> bool {
    match &node.tag {
        Tag::Explicit(tag) if tag == "!!bool" => text(node).eq_ignore_ascii_case("true"),
        Tag::Implicit if node.style == Style::Any => {
            matches!(node.kind, Kind::Scalar(_))
                && resolve(text(node)) == Resolved::Bool
                && text(node).eq_ignore_ascii_case("true")
        }
        _ => false,
    }
}

/// The suppression comments of one service.
#[derive(Default)]
struct Comments {
    /// Comments on the service key and its direct child keys.
    service: Vec<String>,
    /// Comments on deeper lines.
    nested: Vec<String>,
}

fn parse_suppressions(comments: &[String]) -> (Vec<String>, usize) {
    let mut allowed = Vec::new();
    let mut bad = 0;
    for comment in comments {
        for caps in SUPPRESSION.captures_iter(comment) {
            let reason = caps.get(2).map_or("", |m| m.as_str().trim());
            if reason.is_empty() {
                bad += 1;
            } else {
                allowed.push(caps[1].to_string());
            }
        }
    }
    (allowed, bad)
}

fn lint_service(file: &str, name: &str, service: &Node, comments: &Comments) -> Vec<Finding> {
    let (mut allow, bad) = parse_suppressions(&comments.service);
    allow.extend(parse_suppressions(&comments.nested).0);
    let mut findings = Vec::new();
    for _ in 0..bad {
        findings.push(finding(
            file,
            name,
            "bad-suppression",
            "The suppression has no reason.",
            "Add a reason after the suppression rule.",
        ));
    }
    let mut add = |rule: &str, message: &str, hint: &str| {
        if !allow.iter().any(|a| a == rule) {
            findings.push(finding(file, name, rule, message, hint));
        }
    };

    if let Some(node) = mapping_value(service, "privileged") {
        let interpolated = text(node).contains('$');
        if is_true(node) || interpolated {
            let message = if interpolated {
                "The privileged value uses interpolation, so its safety cannot be determined."
            } else {
                "The service uses privileged mode."
            };
            add("privileged", message, "Remove privileged mode.");
        }
    }
    if let Some(node) = mapping_value(service, "volumes") {
        for volume in sequence_values(node) {
            if docker_socket(volume) {
                add(
                    "docker-socket",
                    "The service mounts the Docker socket.",
                    "Remove the Docker socket mount.",
                );
            }
        }
    }
    for key in ["network_mode", "pid", "ipc"] {
        if let Some(node) = mapping_value(service, key) {
            let interpolated = text(node).contains('$');
            if text(node).trim().eq_ignore_ascii_case("host") || interpolated {
                let message = if interpolated {
                    "The namespace value uses interpolation, so its safety cannot be determined."
                } else {
                    "The service shares a host namespace."
                };
                add("host-namespace", message, "Use an isolated namespace.");
            }
        }
    }
    if let Some(node) = mapping_value(service, "cap_add") {
        for capability in sequence_values(node) {
            let upper = text(capability).trim().to_uppercase();
            let value = upper.strip_prefix("CAP_").unwrap_or(&upper);
            if value == "ALL" || value == "SYS_ADMIN" {
                add(
                    "dangerous-cap",
                    "The service adds a dangerous Linux capability.",
                    "Remove the dangerous capability.",
                );
            }
        }
    }
    if let Some(node) = mapping_value(service, "security_opt") {
        for option in sequence_values(node) {
            let value = text(option).trim().to_lowercase();
            if matches!(
                value.as_str(),
                "seccomp:unconfined"
                    | "seccomp=unconfined"
                    | "apparmor:unconfined"
                    | "apparmor=unconfined"
            ) {
                add(
                    "unconfined",
                    "The service disables a security profile.",
                    "Use the default security profile.",
                );
            }
        }
    }
    if let Some(node) = mapping_value(service, "ports")
        && !public_allowed(service)
    {
        for port in sequence_values(node) {
            if public_port(port) {
                add(
                    "public-port",
                    "The service publishes a public port.",
                    "Bind the port to localhost or add spinup.public: \"true\".",
                );
            }
        }
    }
    if let Some(image) = mapping_value(service, "image")
        && mapping_value(service, "build").is_none()
        && !text(image).contains("@sha256:")
    {
        add(
            "unpinned-image",
            "The service image is not pinned by digest.",
            "Pin the image with an @sha256 digest.",
        );
    }
    if let Some(environment) = mapping_value(service, "environment") {
        for (key, value) in environment_values(environment) {
            if literal_secret(file, &key, &value) {
                add(
                    "literal-secret",
                    "The service sets a literal secret value.",
                    "Use an environment interpolation or a _FILE variable.",
                );
            }
        }
    }
    findings
}

fn finding(file: &str, service: &str, rule: &str, message: &str, hint: &str) -> Finding {
    Finding {
        file: file.to_string(),
        service: service.to_string(),
        rule: rule.to_string(),
        message: message.to_string(),
        hint: hint.to_string(),
    }
}

fn docker_socket(node: &Node) -> bool {
    if matches!(node.kind, Kind::Map(_)) {
        return mapping_value(node, "source").is_some_and(|source| socket_path(text(source)));
    }
    socket_path(text(node).split(':').next().unwrap_or(""))
}

fn socket_path(value: &str) -> bool {
    let source = clean_path(value);
    if !source.starts_with('/') {
        return false;
    }
    let parent = source.strip_suffix('/').unwrap_or(&source);
    ["/var/run/docker.sock", "/run/docker.sock"]
        .iter()
        .any(|socket| *socket == source || socket.starts_with(&format!("{parent}/")))
}

/// Cleans a slash path lexically (Go `path.Clean`).
pub(crate) fn clean_path(path: &str) -> String {
    if path.is_empty() {
        return ".".to_string();
    }
    let rooted = path.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| *last != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            _ => parts.push(part),
        }
    }
    let joined = parts.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_string(),
        (false, false) => joined,
    }
}

fn public_allowed(service: &Node) -> bool {
    let Some(labels) = mapping_value(service, "labels") else {
        return false;
    };
    if matches!(labels.kind, Kind::Map(_)) {
        return mapping_value(labels, "spinup.public")
            .is_some_and(|value| text(value).eq_ignore_ascii_case("true"));
    }
    sequence_values(labels)
        .iter()
        .any(|label| text(label) == "spinup.public=true")
}

fn public_port(node: &Node) -> bool {
    if matches!(node.kind, Kind::Map(_)) {
        return mapping_value(node, "host_ip").is_none_or(|ip| is_public_host(text(ip)));
    }
    // Split only outside interpolations and IPv6 brackets.
    let parts = port_parts(text(node).trim());
    parts.len() != 3 || is_public_host(parts[0])
}

fn port_parts(value: &str) -> Vec<&str> {
    let mut parts = Vec::new();
    let (mut start, mut braces, mut brackets) = (0, 0u32, 0u32);
    for (i, ch) in value.char_indices() {
        match ch {
            '{' => braces += 1,
            '}' => braces = braces.saturating_sub(1),
            '[' => brackets += 1,
            ']' => brackets = brackets.saturating_sub(1),
            ':' if braces == 0 && brackets == 0 => {
                parts.push(&value[start..i]);
                start = i + 1;
            }
            _ => {}
        }
    }
    parts.push(&value[start..]);
    parts
}

fn is_public_host(host: &str) -> bool {
    let host = host.trim();
    if host.contains('$') {
        return true;
    }
    if host == "localhost" {
        return false;
    }
    let host = host.strip_suffix(']').unwrap_or(host);
    let host = host.strip_prefix('[').unwrap_or(host);
    match host.parse::<IpAddr>() {
        Err(_) => true,
        Ok(IpAddr::V6(ip)) => !ip
            .to_ipv4_mapped()
            .map_or(ip.is_loopback(), |v4| v4.is_loopback()),
        Ok(ip) => !ip.is_loopback(),
    }
}

fn environment_values(node: &Node) -> Vec<(String, String)> {
    match &node.kind {
        Kind::Map(entries) => entries
            .iter()
            .map(|(k, v)| (text(k).to_string(), text(v).to_string()))
            .collect(),
        Kind::Seq(items) => items
            .iter()
            .filter_map(|entry| {
                text(entry)
                    .split_once('=')
                    .map(|(k, v)| (k.to_string(), v.to_string()))
            })
            .collect(),
        Kind::Scalar(_) | Kind::Alias(_) => Vec::new(),
    }
}

fn literal_secret(file: &str, key: &str, value: &str) -> bool {
    let base = file.rsplit(['/', '\\']).next().unwrap_or(file);
    let dev = base == "compose.dev.yml" || base == "compose.dev.yaml";
    if SECRET_NAME.is_match(key)
        && !key.to_uppercase().ends_with("_FILE")
        && !value.is_empty()
        && literal_value(value, dev)
    {
        return true;
    }
    URL_CREDENTIALS.captures_iter(value).any(|caps| {
        caps[1]
            .split_once(':')
            .is_some_and(|(_, password)| !password.is_empty() && literal_value(password, dev))
    })
}

fn literal_value(value: &str, dev: bool) -> bool {
    if !INTERPOLATION.is_match(value) {
        return true;
    }
    !dev && SECRET_DEFAULT
        .captures_iter(value)
        .any(|caps| !caps[1].is_empty())
}

// ---- suppression comments from the source text ----

struct Line<'a> {
    indent: usize,
    code: &'a str,
    comment: Option<&'a str>,
}

impl Line<'_> {
    fn blank(&self) -> bool {
        self.code.is_empty() && self.comment.is_none()
    }

    fn comment_only(&self) -> bool {
        self.code.is_empty() && self.comment.is_some()
    }
}

/// Splits a source line into code and a trailing comment. A `#` starts a comment at the
/// line start or after white space, outside quotes.
fn split_comment(raw: &str) -> (&str, Option<&str>) {
    let (mut single, mut double) = (false, false);
    let mut previous = ' ';
    for (i, ch) in raw.char_indices() {
        match ch {
            '\'' if !double => single = !single,
            '"' if !single && previous != '\\' => double = !double,
            '#' if !single && !double && previous.is_whitespace() => {
                return (raw[..i].trim_end(), Some(raw[i..].trim_end()));
            }
            _ => {}
        }
        previous = ch;
    }
    (raw.trim_end(), None)
}

fn scan_lines(src: &str) -> Vec<Line<'_>> {
    let mut lines = Vec::new();
    let mut block_indent: Option<usize> = None;
    for raw in src.lines() {
        let indent = raw.len() - raw.trim_start().len();
        if let Some(parent) = block_indent {
            if raw.trim().is_empty() || indent > parent {
                // Block scalar content is neither code nor comment.
                let code = if raw.trim().is_empty() { "" } else { "\u{0}" };
                lines.push(Line {
                    indent,
                    code,
                    comment: None,
                });
                continue;
            }
            block_indent = None;
        }
        let (code, comment) = split_comment(raw);
        let code = code.trim_start();
        if BLOCK_SCALAR.is_match(code) {
            block_indent = Some(indent);
        }
        lines.push(Line {
            indent,
            code,
            comment,
        });
    }
    lines
}

fn key_name(code: &str) -> Option<String> {
    if code.starts_with("- ") || code == "-" {
        return None;
    }
    if let Some(rest) = code.strip_prefix(['"', '\'']) {
        let quote = code.chars().next()?;
        let end = rest.find(quote)?;
        return Some(rest[..end].to_string());
    }
    let mut chars = code.char_indices().peekable();
    while let Some((i, ch)) = chars.next() {
        if ch == ':' && chars.peek().is_none_or(|(_, next)| next.is_whitespace()) {
            return Some(code[..i].trim_end().to_string());
        }
    }
    None
}

fn service_comments(src: &str, name: &str) -> Comments {
    let lines = scan_lines(src);
    let mut out = Comments::default();
    // The `services:` block.
    let Some(start) = lines
        .iter()
        .position(|l| l.indent == 0 && key_name(l.code).as_deref() == Some("services"))
    else {
        return out;
    };
    let end = lines[start + 1..]
        .iter()
        .position(|l| l.indent == 0 && !l.code.is_empty())
        .map_or(lines.len(), |p| start + 1 + p);
    let block = &lines[start + 1..end];
    let Some(service_indent) = block.iter().find(|l| !l.code.is_empty()).map(|l| l.indent) else {
        return out;
    };
    let is_key = |l: &Line<'_>| l.indent == service_indent && key_name(l.code).is_some();
    let Some(first) = block
        .iter()
        .position(|l| is_key(l) && key_name(l.code).as_deref() == Some(name))
    else {
        return out;
    };
    let last = block[first + 1..]
        .iter()
        .position(is_key)
        .map_or(block.len(), |p| first + 1 + p);

    // The head comment of the service key.
    let mut head: Vec<&str> = Vec::new();
    let mut i = first;
    while i > 0 && block[i - 1].comment_only() {
        i -= 1;
        head.push(block[i].comment.unwrap_or(""));
    }
    if !head.is_empty() {
        head.reverse();
        out.service.push(head.join("\n"));
    }
    if let Some(comment) = block[first].comment {
        out.service.push(comment.to_string());
    }

    let region = &block[first + 1..last];
    let child_indent = region.iter().find(|l| !l.code.is_empty()).map(|l| l.indent);
    let direct =
        |l: &Line<'_>| Some(l.indent) == child_indent && !l.code.starts_with("- ") && l.code != "-";
    let mut pending: Vec<&str> = Vec::new();
    for line in region {
        if line.comment_only() {
            pending.push(line.comment.unwrap_or(""));
            continue;
        }
        if line.blank() {
            pending.clear();
            continue;
        }
        let target = if direct(line) {
            &mut out.service
        } else {
            &mut out.nested
        };
        if !pending.is_empty() {
            target.push(pending.join("\n"));
            pending.clear();
        }
        if let Some(comment) = line.comment {
            target.push(comment.to_string());
        }
    }
    out
}

// ---- the doctor check ----

/// The `compose-security` check.
// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[distributed_slice(CHECKS)]
static COMPOSE_SECURITY: Check = Check {
    id: "compose-security",
    title: "Compose security lint",
    scope: Scope::Local,
    quick: true,
    run: run_check,
};

fn compose_files(root: &std::path::Path) -> std::io::Result<Vec<std::path::PathBuf>> {
    let deploy = root.join("deploy");
    let mut files = Vec::new();
    let entries = match std::fs::read_dir(&deploy) {
        Ok(entries) => entries,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(files),
        Err(e) => return Err(e),
    };
    for entry in entries {
        let entry = entry?;
        let name = entry.file_name();
        let Some(name) = name.to_str() else { continue };
        if name.starts_with("compose") && (name.ends_with(".yml") || name.ends_with(".yaml")) {
            files.push(entry.path());
        }
    }
    files.sort();
    Ok(files)
}

fn run_check(ctx: &Context) -> Outcome {
    let files = match compose_files(&ctx.root) {
        Ok(files) => files,
        Err(e) => {
            return Outcome::fail(
                format!("failed to find Compose files: {e}"),
                "Check the project path.",
            );
        }
    };
    if files.is_empty() {
        return Outcome::new(Status::Skip, "No Compose files found.");
    }
    let mut findings = Vec::new();
    for path in files {
        let relative = path
            .strip_prefix(&ctx.root)
            .unwrap_or(&path)
            .to_string_lossy()
            .replace('\\', "/");
        let data = match std::fs::read_to_string(&path) {
            Ok(data) => data,
            Err(e) => {
                return Outcome::fail(
                    format!("failed to read {relative}: {e}"),
                    "Check the Compose file permissions.",
                );
            }
        };
        match lint(&relative, &data) {
            Ok(found) => findings.extend(found),
            Err(e) => {
                return Outcome::fail(
                    format!("failed to parse {relative}: {e}"),
                    "Fix the Compose YAML syntax.",
                );
            }
        }
    }
    if findings.is_empty() {
        return Outcome::new(Status::Ok, "Compose security lint passed.");
    }
    let mut lines: Vec<String> = findings
        .iter()
        .take(10)
        .map(|f| format!("{}: {}: {}: {}", f.file, f.service, f.rule, f.message))
        .collect();
    if findings.len() > 10 {
        lines.push(format!("{} more findings.", findings.len() - 10));
    }
    Outcome::fail(lines.join("\n"), findings[0].hint.clone())
}

#[cfg(test)]
mod tests;
