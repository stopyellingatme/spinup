// Code maintained by the main loop. DO NOT EDIT in a unit.
//
// Blank imports pull self-registering packages (doctor checks, generators,
// deploy hooks, and similar) into the binary. The pre-seed commit of each
// milestone adds the packages that the milestone creates.

package main

import (
	// M1
	_ "github.com/stopyellingatme/spinup/internal/doctor/composelint"
)
