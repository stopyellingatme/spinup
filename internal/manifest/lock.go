package manifest

// SchemaVersion is the current spinup.lock schema.
const SchemaVersion = 1

// currentSchemaVersion allows tests to exercise future schema migrations.
var currentSchemaVersion = SchemaVersion

// Class determines how an upgrade treats a generated file.
type Class string

const (
	// Managed files are maintained by the CLI.
	Managed Class = "managed"
	// Scaffold files are merged with user changes during upgrades.
	Scaffold Class = "scaffold"
	// Seed files are written once and never compared during upgrades.
	Seed Class = "seed"
)

// Valid reports whether c is a supported file class.
func (c Class) Valid() bool {
	return c == Managed || c == Scaffold || c == Seed
}

// KitRef identifies the runtime module and its version.
type KitRef struct {
	// Module is the runtime module path.
	Module string `json:"module"`
	// Version is the runtime module version.
	Version string `json:"version"`
}

// Plugin identifies a locked plugin and its API version.
type Plugin struct {
	// Name is the plugin's registration name.
	Name string `json:"name"`
	// Version is the locked plugin version.
	Version string `json:"version"`
	// SHA256 is the plugin artifact checksum.
	SHA256 string `json:"sha256"`
	// API is the plugin protocol version.
	API int `json:"api"`
}

// FileEntry records a generated file's class, source, content hash, and permissions.
// Seed files may omit the template, hash, and mode.
type FileEntry struct {
	// Class specifies the file's upgrade policy.
	Class Class `json:"class"`
	// Template is the relative path of the source template.
	Template string `json:"template,omitempty"`
	// Hash is the normalized content hash with the sha256: prefix.
	Hash string `json:"hash,omitempty"`
	// Mode contains four octal permission digits, such as 0644.
	Mode string `json:"mode,omitempty"`
}

// Lock records the inputs and file state needed to reproduce and upgrade an app.
type Lock struct {
	// Schema identifies the lock format.
	Schema int `json:"schema"`
	// CLIVersion is the CLI version that wrote the lock.
	CLIVersion string `json:"cli_version"`
	// TemplateVersion identifies the source template pack.
	TemplateVersion string `json:"template_version"`
	// MinCLI is the minimum CLI version that can operate this app.
	MinCLI string `json:"min_cli"`
	// Kit identifies the runtime module.
	Kit KitRef `json:"kit"`
	// BehaviorVersion identifies the selected runtime behavior.
	BehaviorVersion int `json:"behavior_version"`
	// FormatterGo is the Go version used to format generated code.
	FormatterGo string `json:"formatter_go"`
	// Answers contains the inputs used to render the template pack.
	Answers map[string]any `json:"answers"`
	// StackVersions contains the locked versions of stack components.
	StackVersions map[string]string `json:"stack_versions"`
	// Plugins contains the locked plugins.
	Plugins []Plugin `json:"plugins"`
	// Files maps clean relative paths to tracked file entries.
	Files map[string]FileEntry `json:"files"`
	// Tombstones contains paths that upgrades must not recreate.
	Tombstones []string `json:"tombstones"`
	// Orphans contains files retained after their templates were removed.
	Orphans []string `json:"orphans"`
	// AppliedMigrations contains the identifiers of completed migrations.
	AppliedMigrations []string `json:"applied_migrations"`
}

// New returns an empty lock with the current schema and initialized maps.
func New() *Lock {
	return &Lock{
		Schema:        currentSchemaVersion,
		Answers:       make(map[string]any),
		StackVersions: make(map[string]string),
		Files:         make(map[string]FileEntry),
	}
}
