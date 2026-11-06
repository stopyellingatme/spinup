package flags

// Decl is the declaration of one flag, from the generated file.
// Unit F2 owns this file and adds the rest of ADR 0029 decision 4.3.
type Decl struct {
	Description string
	Default     bool
	Public      bool
	Expires     string // YYYY-MM-DD or empty
}

// Declared maps a key to its declaration. The generated Set is a Declared.
type Declared map[string]Decl
