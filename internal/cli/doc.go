// Package cli wires the cobra commands. It holds no logic.
//
// Commands register themselves from init() with Register. Execute builds the
// root command and runs it.
package cli
