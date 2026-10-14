// Package templates embeds and loads declarative template packs.
//
// A pack resides in packs/<directory>/ and contains pack.toml and source files.
// Its name equals the directory name with one leading underscore removed.
// A leading underscore identifies an internal pack. List includes internal packs.
// Load accepts directory names. Embedded returns a file system rooted at packs.
//
// pack.toml declares name, description, requires, questions, and files.
// requires contains pack names. The caller resolves dependencies and pack order.
// Each question declares key, prompt, type, and an optional default.
// Types are string, bool, and enum. Enum questions require choices.
// String questions can declare a regular expression in pattern.
// Bool defaults accept TOML booleans or the strings "true" and "false".
// HasDefault preserves explicit empty string defaults.
// The modules question uses type string but produces []string answers.
// Its default is a comma-separated string. An empty default produces an empty slice.
//
// Each file declares path, source, and class. Classes are managed, scaffold, and seed.
// Optional fields are when, since, renamed_from, and mode. The default mode is 0644.
// Paths are clean relative paths. Output paths can contain [[ ]] template actions.
// Sources are relative to the pack directory. A .tmpl suffix enables rendering.
// Other sources are copied. The render package normalizes text after copying.
// since and renamed_from are metadata for future upgrades.
//
// pack.d/*.toml fragments append questions and files in filename order.
// Fragments can only declare questions and files. Duplicate keys or paths are errors.
// Duplicate errors identify both metadata files.
//
// When conditions support key == 'value', key != 'value', key, !key,
// and key contains 'value'. Bare keys require bool answers.
// Comparisons require strings. Contains requires []string answers.
// Keys contain ASCII letters, digits, and underscores, with no leading digit.
// Literal values use single quotes without escapes.
// Conditions support && and ||. The && operator binds more tightly.
// Parentheses are unsupported. Load compiles conditions once.
// Evaluation checks all referenced keys and types, even in short-circuited branches.
package templates
