//! Go `time.ParseDuration` and `time.Duration.String`. The error texts of the
//! rate limit name durations in the Go format (`3s`, `24h0m0s`), so the Rust
//! texts are equal to the Go texts.

/// Parses a Go duration such as `3s`, `1h30m`, `1.5h`, or `250ms` into
/// nanoseconds. It returns `None` for a value that Go rejects.
pub fn parse(s: &str) -> Option<i64> {
    let (neg, mut rest) = match s.as_bytes().first()? {
        b'-' => (true, &s[1..]),
        b'+' => (false, &s[1..]),
        _ => (false, s),
    };
    if rest == "0" {
        return Some(0);
    }
    if rest.is_empty() {
        return None;
    }
    let mut total: f64 = 0.0;
    while !rest.is_empty() {
        let num_end = rest
            .find(|c: char| !(c.is_ascii_digit() || c == '.'))
            .unwrap_or(rest.len());
        let num = &rest[..num_end];
        if num.is_empty() || num == "." {
            return None;
        }
        let value: f64 = num.parse().ok()?;
        rest = &rest[num_end..];
        let unit_end = rest
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(rest.len());
        let scale: f64 = match &rest[..unit_end] {
            "ns" => 1.0,
            "us" | "µs" | "μs" => 1e3,
            "ms" => 1e6,
            "s" => 1e9,
            "m" => 60e9,
            "h" => 3600e9,
            _ => return None,
        };
        rest = &rest[unit_end..];
        total += value * scale;
        if total > i64::MAX as f64 {
            return None;
        }
    }
    let ns = total.round() as i64;
    Some(if neg { -ns } else { ns })
}

/// Formats nanoseconds as Go's `Duration.String` does.
pub fn format(d: i64) -> String {
    if d == 0 {
        return "0s".into();
    }
    let sign = if d < 0 { "-" } else { "" };
    let u = d.unsigned_abs();
    let body = if u < 1_000_000_000 {
        let (unit, scale) = if u < 1_000 {
            ("ns", 1)
        } else if u < 1_000_000 {
            ("µs", 1_000)
        } else {
            ("ms", 1_000_000)
        };
        format!("{}{unit}", frac(u, scale))
    } else {
        let h = u / 3_600_000_000_000;
        let m = u / 60_000_000_000 % 60;
        let s = format!("{}s", frac(u % 60_000_000_000, 1_000_000_000));
        if h > 0 {
            format!("{h}h{m}m{s}")
        } else if m > 0 {
            format!("{m}m{s}")
        } else {
            s
        }
    };
    format!("{sign}{body}")
}

fn frac(v: u64, scale: u64) -> String {
    let int = v / scale;
    let rem = v % scale;
    if rem == 0 {
        return int.to_string();
    }
    let width = scale.ilog10() as usize;
    let digits = format!("{rem:0width$}");
    format!("{int}.{}", digits.trim_end_matches('0'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn format_matches_go() {
        for (ns, want) in [
            (0, "0s"),
            (1_000_000, "1ms"),
            (50_000_000, "50ms"),
            (1_500_000, "1.5ms"),
            (3_000_000_000, "3s"),
            (20_000_000_000, "20s"),
            (90_000_000_000, "1m30s"),
            (3_600_000_000_000, "1h0m0s"),
            (86_400_000_000_000, "24h0m0s"),
            (604_800_000_000_000, "168h0m0s"),
            (1_500, "1.5µs"),
            (-3_000_000_000, "-3s"),
        ] {
            assert_eq!(format(ns), want, "{ns}");
        }
    }

    #[test]
    fn parse_matches_go() {
        for (s, want) in [
            ("3s", Some(3_000_000_000)),
            ("1h", Some(3_600_000_000_000)),
            ("1h30m", Some(5_400_000_000_000)),
            ("1.5h", Some(5_400_000_000_000)),
            ("250ms", Some(250_000_000)),
            ("0", Some(0)),
            ("0s", Some(0)),
            ("-1s", Some(-1_000_000_000)),
            ("3", None),
            ("", None),
            ("s", None),
            ("1d", None),
        ] {
            assert_eq!(parse(s), want, "{s}");
        }
    }
}
