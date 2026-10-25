//! The generator registry: the static slice plus the plugin generators (Go
//! `internal/gen/registry`).
//!
//! Built-in generators register through the [`GENERATORS`] linkme slice. The slice has no
//! central list, so the registry checks the names when it reads the slice: a name or alias
//! must not be empty, and it must not repeat. Go panics at registration. Rust reports the
//! problem as a [`RegistryError`] when the CLI builds the registry.
//!
//! [`Registry::extend`] adds generators from another source, such as the declarative
//! generators of plugins (R4), with the same checks.

use crate::contract::{GENERATORS, Generator};

/// A problem with the registered generator set. It is a defect of the build or of a plugin.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum RegistryError {
    /// A generator has an empty name or an empty alias.
    #[error(
        "generator registry: generator {generator:?} has an empty name or alias\nTry: give each generator a name and non-empty aliases"
    )]
    Empty {
        /// The generator name (empty when the name is the empty value).
        generator: String,
    },
    /// Two generators (or one generator twice) use the same name or alias.
    #[error(
        "generator registry: the name or alias {name:?} is used twice\nTry: give each generator a unique name and unique aliases"
    )]
    Duplicate {
        /// The repeated name or alias.
        name: String,
    },
}

/// The registered generators, sorted by name. The registry holds references, so it never
/// copies a generator.
#[derive(Debug, Clone, Default)]
pub struct Registry<'a> {
    generators: Vec<&'a Generator>,
}

impl Registry<'static> {
    /// Returns the built-in generators of the [`GENERATORS`] slice.
    ///
    /// # Errors
    /// Returns a [`RegistryError`] when a name or alias is empty or used twice.
    pub fn builtin() -> Result<Self, RegistryError> {
        Self::new(GENERATORS.iter())
    }
}

impl<'a> Registry<'a> {
    /// Returns a registry of `generators`, sorted by name.
    ///
    /// # Errors
    /// Returns a [`RegistryError`] when a name or alias is empty or used twice. The check
    /// order is the input order, so the error names the first problem.
    pub fn new(generators: impl IntoIterator<Item = &'a Generator>) -> Result<Self, RegistryError> {
        let mut registry = Self::default();
        registry.extend(generators)?;
        Ok(registry)
    }

    /// Adds `generators` to the registry. A failure leaves the registry unchanged.
    ///
    /// # Errors
    /// Returns a [`RegistryError`] when a name or alias is empty, or when it is already
    /// in the registry or in `generators`.
    pub fn extend(
        &mut self,
        generators: impl IntoIterator<Item = &'a Generator>,
    ) -> Result<(), RegistryError> {
        let mut seen: std::collections::BTreeSet<&str> =
            self.generators.iter().flat_map(|g| names(g)).collect();
        let mut added = Vec::new();
        for generator in generators {
            for name in names(generator) {
                if name.is_empty() {
                    return Err(RegistryError::Empty {
                        generator: generator.name.to_string(),
                    });
                }
                if !seen.insert(name) {
                    return Err(RegistryError::Duplicate {
                        name: name.to_string(),
                    });
                }
            }
            added.push(generator);
        }
        self.generators.extend(added);
        // A stable sort keeps the input order of equal names, but names are unique here.
        self.generators.sort_by(|a, b| a.name.cmp(b.name));
        Ok(())
    }

    /// Returns the generators, sorted by name.
    pub fn all(&self) -> &[&'a Generator] {
        &self.generators
    }

    /// Finds a generator by its name or by one of its aliases.
    pub fn lookup(&self, name: &str) -> Option<&'a Generator> {
        self.generators
            .iter()
            .copied()
            .find(|g| g.name == name || g.aliases.contains(&name))
    }

    /// Returns the number of generators.
    pub fn len(&self) -> usize {
        self.generators.len()
    }

    /// Returns true when no generator is registered.
    pub fn is_empty(&self) -> bool {
        self.generators.is_empty()
    }
}

/// The name of a generator, followed by its aliases.
fn names(generator: &Generator) -> impl Iterator<Item = &'static str> {
    std::iter::once(generator.name).chain(generator.aliases.iter().copied())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::{Input, Output};

    fn run(_: &Input<'_>) -> anyhow::Result<Output> {
        Ok(Output::default())
    }

    const fn generator(name: &'static str, aliases: &'static [&'static str]) -> Generator {
        Generator {
            name,
            aliases,
            short: "",
            usage: "",
            args: std::convert::identity,
            run,
        }
    }

    static ZEBRA: Generator = generator("zebra", &["z"]);
    static ALPHA: Generator = generator("alpha", &[]);

    #[test]
    fn sorts_and_looks_up_names_and_aliases() {
        let registry = Registry::new([&ZEBRA, &ALPHA]).unwrap();
        let names: Vec<_> = registry.all().iter().map(|g| g.name).collect();
        assert_eq!(names, ["alpha", "zebra"]);
        assert_eq!(registry.len(), 2);
        assert!(!registry.is_empty());
        for name in ["z", "zebra"] {
            assert_eq!(registry.lookup(name).map(|g| g.name), Some("zebra"));
        }
        assert!(registry.lookup("missing").is_none());
    }

    #[test]
    fn rejects_empty_and_duplicate_names() {
        static EMPTY: Generator = generator("", &[]);
        static ALPHA_AGAIN: Generator = generator("alpha", &[]);
        static Z_NAME: Generator = generator("z", &[]);
        static ALIAS_ALPHA: Generator = generator("beta", &["alpha"]);
        static EMPTY_ALIAS: Generator = generator("beta", &[""]);
        static SELF_ALIAS: Generator = generator("beta", &["beta"]);
        static TWICE: Generator = generator("beta", &["b", "b"]);
        let base = Registry::new([&ZEBRA, &ALPHA]).unwrap();
        for bad in [
            &EMPTY,
            &ALPHA_AGAIN,
            &Z_NAME,
            &ALIAS_ALPHA,
            &EMPTY_ALIAS,
            &SELF_ALIAS,
            &TWICE,
        ] {
            let mut registry = base.clone();
            let err = registry.extend([bad]).unwrap_err();
            assert!(err.to_string().contains("\nTry: "), "{err}");
            assert_eq!(registry.len(), 2, "a failed extend changed the registry");
        }
        assert!(matches!(
            Registry::new([&ALPHA, &ALPHA]),
            Err(RegistryError::Duplicate { name }) if name == "alpha"
        ));
        assert!(matches!(
            Registry::new([&EMPTY]),
            Err(RegistryError::Empty { .. })
        ));
    }

    #[test]
    fn builtin_slice_is_valid() {
        let registry = Registry::builtin().unwrap();
        assert_eq!(registry.len(), GENERATORS.len());
    }
}
