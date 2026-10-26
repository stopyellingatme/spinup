//! Go `strconv.ParseBool`.

/// Parses a boolean as Go `strconv.ParseBool` does. `1`, `t`, `T`, `TRUE`, `true`, and
/// `True` are true. `0`, `f`, `F`, `FALSE`, `false`, and `False` are false. Every other text
/// gives `None`.
pub fn parse_bool(text: &str) -> Option<bool> {
    match text {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}
