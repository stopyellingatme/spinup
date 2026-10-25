// Package scripttest runs the CLI scripts in testdata/script.
//
// SPINUP_BIN selects a foreign binary. Relative paths start at the repository root.
// The harness copies this binary as spinup and omits the Go build.
// Without SPINUP_BIN, the harness builds the Go CLI.
// [impl:go] selects the Go build. [impl:rust] selects the foreign binary.
//
// SPINUP_SCRIPT_RUN selects script names with a glob.
// In foreign mode, testdata/script/parity.txt limits the eligible scripts.
// Each nonempty line contains one script name or glob, without .txtar.
// Comments start with #. An empty manifest selects no scripts.
// An absent manifest applies no additional filter before the R1 pre-seed.
// Rust scripts in testdata/script/rust replace shared scripts with the same name.
// Go mode ignores the manifest and the Rust directory.
// SPINUP_SCRIPT_PARALLEL limits concurrent scripts to a positive integer.
// Without this variable, the Go test runner controls concurrency.
//
// mkversion <version> [overlay-dir] copies packs and stackversions.yaml into
// $WORK/templates/<version>. The overlay can replace files or list removals in DELETE.
// SPINUP_PACKS_SRC selects the source directory for packs.
// SPINUP_STACK_SRC selects the source file for stack versions.
// Both variables default to their Go sources in the repository.
// Relative source paths start at the repository root.
//
// In foreign mode, SPINUP_KIT_DIR replaces $SPINUP_REPO/kit in
// SPINUP_KIT_REPLACE assignments. Empty assignments keep their meaning.
// The harness rewrites script commands and preserves archived fixture files.
// CARGO_HOME and RUSTUP_HOME retain their absolute paths when HOME changes to $WORK.
package scripttest
