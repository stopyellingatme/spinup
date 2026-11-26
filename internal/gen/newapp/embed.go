package newapp

import "errors"

// embedRefusals maps each module that embed mode cannot hold to its complete error (ADR 0039, section 10.4).
// The pages of these modules still need a SvelteKit server. Every other module passes the check.
var embedRefusals = map[string]string{
	"apikeys": "cannot use apikeys in embed mode: the API key pages require a SvelteKit server\nTry: use --frontend-mode node for apikeys",
	"billing": "cannot use billing in embed mode: the billing pages require a SvelteKit server\nTry: use --frontend-mode node for billing",
	"audit":   "cannot use audit in embed mode: the history pages require a SvelteKit server\nTry: use --frontend-mode node for audit",
	"mcp":     "cannot use mcp in embed mode: the module requires apikeys\nTry: use --frontend-mode node for mcp",
}

// EmbedModuleError returns the refusal of one module in embed mode, or nil when embed mode can hold it.
func EmbedModuleError(name string) error {
	if text, ok := embedRefusals[name]; ok {
		return errors.New(text)
	}
	return nil
}

// CheckEmbedModules applies the module policy of embed mode. It is a pure function: both
// spinup new and spinup module add call it. Outside embed mode it accepts every module.
// It checks the requested names first, in their order, so a request for mcp names mcp and
// not its dependency apikeys. Then it checks the resolved names, such as the dependencies.
func CheckEmbedModules(frontendMode any, requested, resolved []string) error {
	if frontendMode != "embed" {
		return nil
	}
	for _, names := range [][]string{requested, resolved} {
		for _, name := range names {
			if err := EmbedModuleError(name); err != nil {
				return err
			}
		}
	}
	return nil
}
