//! Go `time.ParseDuration` and `time.Duration.String`.
//!
//! The settings use the Go duration text (`20s`, `1h30m`, `250ms`), and error texts name
//! durations in the Go form (`3s`, `24h0m0s`). A Go duration is a signed count of
//! nanoseconds, so [`parse`] and [`format`] use `i64`. [`parse_std`] and [`format_std`]
//! convert from and to [`std::time::Duration`].

use std::time::Duration;

const LIMIT: u64 = 1 << 63;

/// Parses a Go duration such as `3s`, `-1.5h`, `1h30m`, or `250ms` into nanoseconds. It
/// returns `None` for every text that Go rejects, such as `3` (no unit), `1d`, or a value
/// that does not fit in `i64`.
pub fn parse(text: &str) -> Option<i64> {
    let mut s = text;
    let mut neg = false;
    if let Some(c) = s.as_bytes().first()
        && (*c == b'-' || *c == b'+')
    {
        neg = *c == b'-';
        s = &s[1..];
    }
    if s == "0" {
        return Some(0);
    }
    if s.is_empty() {
        return None;
    }
    let mut d: u64 = 0;
    while !s.is_empty() {
        let first = s.as_bytes()[0];
        if !(first == b'.' || first.is_ascii_digit()) {
            return None;
        }
        let before = s.len();
        let (mut v, rest) = leading_int(s)?;
        s = rest;
        let pre = before != s.len();
        let mut post = false;
        let (mut f, mut scale) = (0u64, 1f64);
        if let Some(rest) = s.strip_prefix('.') {
            let before = rest.len();
            let (x, sc, rest) = leading_fraction(rest);
            f = x;
            scale = sc;
            s = rest;
            post = before != s.len();
        }
        if !pre && !post {
            return None;
        }
        let end = s
            .find(|c: char| c == '.' || c.is_ascii_digit())
            .unwrap_or(s.len());
        if end == 0 {
            return None;
        }
        let unit: u64 = match &s[..end] {
            "ns" => 1,
            "us" | "\u{b5}s" | "\u{3bc}s" => 1_000,
            "ms" => 1_000_000,
            "s" => 1_000_000_000,
            "m" => 60_000_000_000,
            "h" => 3_600_000_000_000,
            _ => return None,
        };
        s = &s[end..];
        if v > LIMIT / unit {
            return None;
        }
        v *= unit;
        if f > 0 {
            // Go computes the fraction part in float64 too.
            #[allow(
                clippy::cast_precision_loss,
                clippy::cast_possible_truncation,
                clippy::cast_sign_loss
            )]
            let part = (f as f64 * (unit as f64 / scale)) as u64;
            v = v.checked_add(part)?;
            if v > LIMIT {
                return None;
            }
        }
        d = d.checked_add(v)?;
        if d > LIMIT {
            return None;
        }
    }
    if neg {
        // d <= 1 << 63, so the negative value fits.
        return Some(0i64.wrapping_sub_unsigned(d));
    }
    i64::try_from(d).ok()
}

/// Reads the leading digits of `s`. It returns `None` on overflow (Go `errLeadingInt`).
fn leading_int(s: &str) -> Option<(u64, &str)> {
    let mut x: u64 = 0;
    let mut i = 0;
    for c in s.bytes() {
        if !c.is_ascii_digit() {
            break;
        }
        if x > LIMIT / 10 {
            return None;
        }
        x = x * 10 + u64::from(c - b'0');
        if x > LIMIT {
            return None;
        }
        i += 1;
    }
    Some((x, &s[i..]))
}

/// Reads the digits of a fraction. On overflow it keeps the digits read so far.
fn leading_fraction(s: &str) -> (u64, f64, &str) {
    let mut x: u64 = 0;
    let mut scale = 1f64;
    let mut overflow = false;
    let mut i = 0;
    for c in s.bytes() {
        if !c.is_ascii_digit() {
            break;
        }
        i += 1;
        if overflow {
            continue;
        }
        if x > (LIMIT - 1) / 10 {
            overflow = true;
            continue;
        }
        let y = x * 10 + u64::from(c - b'0');
        if y > LIMIT {
            overflow = true;
            continue;
        }
        x = y;
        scale *= 10.0;
    }
    (x, scale, &s[i..])
}

/// Formats nanoseconds as Go `Duration.String` does: `0s`, `1.5µs`, `250ms`, `20s`,
/// `1m30s`, `24h0m0s`.
pub fn format(ns: i64) -> String {
    if ns == 0 {
        return "0s".into();
    }
    let sign = if ns < 0 { "-" } else { "" };
    let u = ns.unsigned_abs();
    let body = if u < 1_000_000_000 {
        let (unit, scale) = if u < 1_000 {
            ("ns", 1)
        } else if u < 1_000_000 {
            ("\u{b5}s", 1_000)
        } else {
            ("ms", 1_000_000)
        };
        format!("{}{unit}", fraction(u, scale))
    } else {
        let h = u / 3_600_000_000_000;
        let m = u / 60_000_000_000 % 60;
        let s = format!("{}s", fraction(u % 60_000_000_000, 1_000_000_000));
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

fn fraction(v: u64, scale: u64) -> String {
    let int = v / scale;
    let rem = v % scale;
    if rem == 0 {
        return int.to_string();
    }
    let width = scale.ilog10() as usize;
    let digits = format!("{rem:0width$}");
    format!("{int}.{}", digits.trim_end_matches('0'))
}

/// Parses a Go duration into a [`Duration`]. A negative duration gives `None`.
pub fn parse_std(text: &str) -> Option<Duration> {
    let ns = u64::try_from(parse(text)?).ok()?;
    Some(Duration::from_nanos(ns))
}

/// Formats a [`Duration`] as Go `Duration.String` does. A duration above the Go range
/// (about 292 years) is clamped to the largest Go duration.
pub fn format_std(d: Duration) -> String {
    format(i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
}
