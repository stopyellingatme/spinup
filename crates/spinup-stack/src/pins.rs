//! Pin helpers and the `ci_pins_test` port (digest pins, SHA pins, 14-day rule).
//!
//! The checks scan text, so the tests can run them on the repository workflows and on the
//! pack templates. Each check returns its problems as text. An empty list means that the
//! pins are correct.
//! - Action pins: a literal `uses: owner/repo[/path]@ref` line uses the commit SHA of
//!   `actions.<owner/repo>` (ADR 0018, the `docs.yml` defect).
//! - Image pins: a literal `image:` reference has the tag and the digest of a pinned image.
//!   A `${...}` reference needs the marker `spinup:allow unpinned-image`.
//! - The 14-day rule: a pin whose comment names a release date less than 14 days before the
//!   verification date must state its exception (`Exception to the 14-day rule` or `recheck`).

use std::sync::LazyLock;

use regex::Regex;

use crate::stack::Stack;

/// The minimum age of a pinned release, in days, on its verification date.
pub const MIN_RELEASE_AGE_DAYS: i64 = 14;

/// The marker that allows an `image:` line without a digest.
pub const ALLOW_UNPINNED_IMAGE: &str = "spinup:allow unpinned-image";

/// A literal `uses: owner/repo[/path]@ref` line. A local action (`./...`) does not match.
static USES: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^\s*(?:-\s+)?uses:\s*([A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+)@(\S+)").expect("regex")
});

/// An `image:` line of a workflow or a Compose file.
static IMAGE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^\s*(?:-\s+)?image:\s*(\S+)").expect("regex"));

/// A pin line with a release date in its comment.
static RELEASED: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"#.*\breleased (\d{4}-\d{2}-\d{2})").expect("regex"));

/// The date of a dated pin decision in a comment (`pinned 2026-10-15 to ...`).
static PINNED: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"#.*\bpinned (\d{4}-\d{2}-\d{2})").expect("regex"));

/// Reports whether `value` is an image digest, `sha256:<64 lowercase hex>`.
pub fn is_digest(value: &str) -> bool {
    value.strip_prefix("sha256:").is_some_and(is_sha256)
}

/// Reports whether `value` is a SHA-256 digest, 64 lowercase hex characters.
pub fn is_sha256(value: &str) -> bool {
    is_lower_hex(value, 64)
}

/// Reports whether `value` is a full git commit SHA, 40 lowercase hex characters.
pub fn is_commit_sha(value: &str) -> bool {
    is_lower_hex(value, 40)
}

fn is_lower_hex(value: &str, len: usize) -> bool {
    value.len() == len
        && value
            .bytes()
            .all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

/// Returns the stack key of an action: `owner/repo`, without a sub-path.
pub fn action_key(name: &str) -> &str {
    match name.match_indices('/').nth(1) {
        Some((index, _)) => &name[..index],
        None => name,
    }
}

/// The result of one scan: the number of checked pins and the problems.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct Scan {
    /// The number of pins that the scan checked.
    pub checked: usize,
    /// One line for each wrong pin, with the file and the line number.
    pub problems: Vec<String>,
}

/// Checks the literal `uses:` lines of one file against the action pins.
///
/// Lines with a template expression (`[[`) are skipped, as in Go.
pub fn scan_action_pins(stack: &Stack, file: &str, text: &str) -> Scan {
    let mut scan = Scan::default();
    for (index, line) in text.split('\n').enumerate() {
        if line.contains("[[") {
            continue;
        }
        let Some(found) = USES.captures(line) else {
            continue;
        };
        let (name, reference) = (&found[1], &found[2]);
        let at = format!("{file}:{}", index + 1);
        let key = action_key(name);
        let Some(pin) = stack.actions.get(key) else {
            scan.problems.push(format!(
                "{at}: action {name} has no pin in stackversions.yaml\nTry: add {key} to actions"
            ));
            continue;
        };
        if reference != pin.sha {
            scan.problems.push(format!(
                "{at}: action {name} uses {reference}, want {} ({})\nTry: use the SHA from stackversions.yaml",
                pin.sha, pin.tag
            ));
        }
        scan.checked += 1;
    }
    scan
}

/// Checks the literal `image:` lines of one file against the image pins.
///
/// A reference with a digest must equal `<image>:<tag>@<digest>` of a pinned image.
/// A reference without a digest needs the marker [`ALLOW_UNPINNED_IMAGE`] on its line.
/// Lines with a template expression (`[[`) are skipped.
pub fn scan_image_pins(stack: &Stack, file: &str, text: &str) -> Scan {
    let mut scan = Scan::default();
    for (index, line) in text.split('\n').enumerate() {
        if line.contains("[[") {
            continue;
        }
        let Some(found) = IMAGE.captures(line) else {
            continue;
        };
        let reference = found[1].trim_matches(['"', '\'']);
        let at = format!("{file}:{}", index + 1);
        if !reference.contains("@sha256:") {
            if !line.contains(ALLOW_UNPINNED_IMAGE) {
                scan.problems.push(format!(
                    "{at}: image {reference} has no digest\nTry: use the reference of a pin in stackversions.yaml, or add the comment {ALLOW_UNPINNED_IMAGE} with the reason"
                ));
            }
            continue;
        }
        if !stack
            .images
            .values()
            .any(|pin| pin.reference() == reference)
        {
            scan.problems.push(format!(
                "{at}: image {reference} is not a pin in stackversions.yaml\nTry: use the image, tag, and digest of a pin in stackversions.yaml"
            ));
        }
        scan.checked += 1;
    }
    scan
}

/// Checks the 14-day rule on the comments of a stack versions file.
///
/// A pin line whose comment names `released YYYY-MM-DD` must be at least
/// [`MIN_RELEASE_AGE_DAYS`] days old on its decision date: the `pinned YYYY-MM-DD` date of
/// the same comment, or else `verified`. A younger pin must state its exception in the
/// comment (`Exception to the 14-day rule` or `recheck`).
pub fn scan_release_ages(text: &str, verified: &str) -> Scan {
    let mut scan = Scan::default();
    let Some(verified_day) = day_number(verified) else {
        scan.problems.push(format!(
            "verified {verified:?} is not a date\nTry: write the verification date as YYYY-MM-DD"
        ));
        return scan;
    };
    for (index, line) in text.split('\n').enumerate() {
        let Some(found) = RELEASED.captures(line) else {
            continue;
        };
        let at = format!("stackversions.yaml:{}", index + 1);
        let Some(released) = day_number(&found[1]) else {
            scan.problems.push(format!(
                "{at}: release date {} is not a date\nTry: write it as YYYY-MM-DD",
                &found[1]
            ));
            continue;
        };
        let decided = PINNED
            .captures(line)
            .and_then(|pinned| day_number(&pinned[1]))
            .unwrap_or(verified_day);
        scan.checked += 1;
        let age = decided - released;
        let excepted = line.contains("Exception to the 14-day rule") || line.contains("recheck");
        if age < MIN_RELEASE_AGE_DAYS && !excepted {
            scan.problems.push(format!(
                "{at}: the pin was {age} days old when it was chosen, and the rule wants {MIN_RELEASE_AGE_DAYS}\nTry: pin the newest release older than {MIN_RELEASE_AGE_DAYS} days, or state the exception in the comment"
            ));
        }
    }
    scan
}

/// Returns the day number of a `YYYY-MM-DD` date (days since 1970-01-01), or `None`.
pub fn day_number(date: &str) -> Option<i64> {
    let bytes = date.as_bytes();
    if bytes.len() != 10 || bytes[4] != b'-' || bytes[7] != b'-' {
        return None;
    }
    let year: i64 = date[0..4].parse().ok()?;
    let month: i64 = date[5..7].parse().ok()?;
    let day: i64 = date[8..10].parse().ok()?;
    let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0;
    let days_in_month = match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if leap => 29,
        2 => 28,
        _ => return None,
    };
    if !(1..=days_in_month).contains(&day) {
        return None;
    }
    // The civil-from-days inverse (Howard Hinnant), for the proleptic Gregorian calendar.
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    Some(era * 146_097 + doe - 719_468)
}

#[cfg(test)]
mod tests {
    use std::fs;
    use std::path::{Path, PathBuf};

    use super::*;
    use crate::load;

    fn repository() -> PathBuf {
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
    }

    /// Lists the files under `dir` whose names end with one of `suffixes`, sorted.
    fn files(dir: &Path, suffixes: &[&str], out: &mut Vec<PathBuf>) {
        let Ok(entries) = fs::read_dir(dir) else {
            return;
        };
        let mut entries: Vec<_> = entries.map(|e| e.expect("read dir").path()).collect();
        entries.sort();
        for path in entries {
            if path.is_dir() {
                files(&path, suffixes, out);
            } else if path
                .file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| suffixes.iter().any(|s| n.ends_with(s)))
            {
                out.push(path);
            }
        }
    }

    /// The workflows of this repository and the YAML files of the converted packs and overlays.
    fn scanned() -> (usize, Vec<PathBuf>) {
        let root = repository();
        let mut workflows = Vec::new();
        files(
            &root.join(".github/workflows"),
            &[".yml", ".yaml"],
            &mut workflows,
        );
        let count = workflows.len();
        let mut all = workflows;
        files(&root.join(".github/actions"), &[".yml", ".yaml"], &mut all);
        let yaml = [".yml", ".yaml", ".yml.tmpl", ".yaml.tmpl"];
        files(&root.join("crates/spinup-packs/packs"), &yaml, &mut all);
        files(&root.join("crates/spinup-packs/overlay"), &yaml, &mut all);
        (count, all)
    }

    fn run(scan: fn(&Stack, &str, &str) -> Scan) -> Scan {
        let stack = load::load().expect("embedded stack versions");
        let (workflows, all) = scanned();
        assert!(
            workflows > 0 && all.len() > workflows,
            "found {workflows} workflows and {} files in total; the search paths are wrong",
            all.len()
        );
        let root = repository();
        let mut total = Scan::default();
        for path in all {
            let text = fs::read_to_string(&path).expect("read file");
            let name = path
                .strip_prefix(&root)
                .unwrap_or(&path)
                .display()
                .to_string();
            let one = scan(&stack, &name, &text);
            total.checked += one.checked;
            total.problems.extend(one.problems);
        }
        total
    }

    #[test]
    fn workflow_action_pins_use_the_stack_sha() {
        let scan = run(scan_action_pins);
        assert!(scan.problems.is_empty(), "{}", scan.problems.join("\n"));
        assert!(
            scan.checked > 0,
            "no pinned actions found; the pattern is wrong"
        );
    }

    #[test]
    fn image_references_use_the_stack_digest() {
        let scan = run(scan_image_pins);
        assert!(scan.problems.is_empty(), "{}", scan.problems.join("\n"));
        assert!(
            scan.checked > 0,
            "no pinned images found; the pattern is wrong"
        );
    }

    #[test]
    fn stack_pins_keep_the_14_day_rule() {
        let stack = load::load().expect("embedded stack versions");
        let scan = scan_release_ages(load::EMBEDDED, &stack.verified);
        assert!(scan.problems.is_empty(), "{}", scan.problems.join("\n"));
        assert!(
            scan.checked > 0,
            "no dated pins found; the pattern is wrong"
        );
    }

    #[test]
    fn action_scan_reports_wrong_and_missing_pins() {
        let stack = load::load().unwrap();
        let checkout = &stack.actions["actions/checkout"].sha;
        let text = format!(
            "steps:\n  - uses: actions/checkout@{checkout}\n  - uses: actions/checkout@v4\n  - uses: nobody/missing/sub@v1\n  - uses: ./.github/actions/local\n  - uses: actions/checkout@[[ x ]]\n"
        );
        let scan = scan_action_pins(&stack, "w.yml", &text);
        assert_eq!(scan.checked, 2);
        assert_eq!(scan.problems.len(), 2, "{:?}", scan.problems);
        assert!(scan.problems[0].starts_with("w.yml:3: action actions/checkout uses v4"));
        assert!(scan.problems[1].contains("action nobody/missing/sub has no pin in stackversions.yaml\nTry: add nobody/missing to actions"));
        assert!(scan.problems.iter().all(|p| p.contains("\nTry: ")));
    }

    #[test]
    fn image_scan_reports_unpinned_and_unknown_images() {
        let stack = load::load().unwrap();
        let postgres = stack.images["postgres"].reference();
        let unknown = format!("postgres:1@sha256:{}", "0".repeat(64));
        let text = format!(
            "a:\n  image: {postgres}\n  image: \"{unknown}\"\n  image: caddy:2\n  image: ${{X}} # {ALLOW_UNPINNED_IMAGE} set by deploy\n  image: [[ image(\"x\") ]]\n"
        );
        let scan = scan_image_pins(&stack, "c.yml", &text);
        assert_eq!(scan.checked, 2);
        assert_eq!(scan.problems.len(), 2, "{:?}", scan.problems);
        assert!(scan.problems[0].contains("is not a pin"));
        assert!(scan.problems[1].contains("caddy:2 has no digest"));
    }

    #[test]
    fn release_age_scan_wants_an_exception_for_young_pins() {
        let text = "a: 1 # released 2026-10-12\nb: 2 # released 2026-10-12, recheck before release\nc: 3 # Exception to the 14-day rule (released 2026-10-10)\nd: 4 # released 2026-09-01\ne: 5 # pinned 2026-10-30 to the newest; released 2026-10-12\n";
        let scan = scan_release_ages(text, "2026-10-13");
        assert_eq!(scan.checked, 5);
        assert_eq!(scan.problems.len(), 1, "{:?}", scan.problems);
        assert!(scan.problems[0].starts_with("stackversions.yaml:1: the pin was 1 days old"));
        assert_eq!(scan_release_ages("", "today").problems.len(), 1);
    }

    #[test]
    fn helpers_check_formats() {
        assert!(is_digest(&format!("sha256:{}", "a".repeat(64))));
        assert!(!is_digest(&format!("sha256:{}", "A".repeat(64))));
        assert!(!is_digest("sha256:bad"));
        assert!(is_commit_sha(&"0".repeat(40)));
        assert!(!is_commit_sha(&"0".repeat(41)));
        assert_eq!(action_key("actions/checkout"), "actions/checkout");
        assert_eq!(
            action_key("github/codeql-action/init"),
            "github/codeql-action"
        );
        assert_eq!(day_number("1970-01-01"), Some(0));
        assert_eq!(
            day_number("2026-10-13")
                .zip(day_number("2026-10-27"))
                .map(|(a, b)| b - a),
            Some(14)
        );
        assert_eq!(
            day_number("2024-02-29")
                .zip(day_number("2024-03-01"))
                .map(|(a, b)| b - a),
            Some(1)
        );
        assert_eq!(day_number("2026-02-29"), None);
        assert_eq!(day_number("2026-13-01"), None);
        assert_eq!(day_number("26-10-13"), None);
    }
}
