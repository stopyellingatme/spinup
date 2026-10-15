package ci

// ManagedFiles returns every path owned by the ci pack, sorted by path.
// Rendering selects the optional workflows from the enabled modules.
// Callers receive a new slice and can change it without changing other callers.
func ManagedFiles() []string {
	return []string{
		".github/actions/setup-spinup/action.yml",
		".github/workflows/backup-verify.yml",
		".github/workflows/ci.yml",
		".github/workflows/deploy.yml",
		".github/workflows/upgrade-check.yml",
	}
}
