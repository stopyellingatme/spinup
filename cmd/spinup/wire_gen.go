// Code maintained by the main loop. DO NOT EDIT in a unit.
//
// Blank imports pull self-registering packages (doctor checks, generators,
// deploy hooks, and similar) into the binary. The pre-seed commit of each
// milestone adds the packages that the milestone creates.

package main

import (
	// M1
	_ "github.com/stopyellingatme/spinup/internal/doctor/composelint"

	// M2
	_ "github.com/stopyellingatme/spinup/internal/doctor/checks"
	_ "github.com/stopyellingatme/spinup/internal/migrate"

	// M3
	_ "github.com/stopyellingatme/spinup/internal/gen/handler"
	_ "github.com/stopyellingatme/spinup/internal/gen/job"
	_ "github.com/stopyellingatme/spinup/internal/gen/migration"
	_ "github.com/stopyellingatme/spinup/internal/gen/model"
	_ "github.com/stopyellingatme/spinup/internal/gen/openapi"
	_ "github.com/stopyellingatme/spinup/internal/gen/resource"

	// M4
	_ "github.com/stopyellingatme/spinup/internal/doctor/clientdrift"
	_ "github.com/stopyellingatme/spinup/internal/doctor/quick"
	_ "github.com/stopyellingatme/spinup/internal/gen/client"
	_ "github.com/stopyellingatme/spinup/internal/gen/component"
	_ "github.com/stopyellingatme/spinup/internal/gen/page"

	// M5
	_ "github.com/stopyellingatme/spinup/internal/backup"
	_ "github.com/stopyellingatme/spinup/internal/doctor/plainsecrets"
	_ "github.com/stopyellingatme/spinup/internal/server"

	// M6
	_ "github.com/stopyellingatme/spinup/internal/gen/alert"
	_ "github.com/stopyellingatme/spinup/internal/gen/dashboard"
	_ "github.com/stopyellingatme/spinup/internal/monitor"

	// M7
	_ "github.com/stopyellingatme/spinup/internal/backup/pgdump"
	_ "github.com/stopyellingatme/spinup/internal/doctor/remote"
	_ "github.com/stopyellingatme/spinup/internal/maintain"
	_ "github.com/stopyellingatme/spinup/internal/maintain/pg"

	// M9
	_ "github.com/stopyellingatme/spinup/internal/gen/mailer"
	_ "github.com/stopyellingatme/spinup/internal/plugin/pack"
	_ "github.com/stopyellingatme/spinup/internal/plugin/render"

	// M10
	_ "github.com/stopyellingatme/spinup/internal/backup/walg"
	_ "github.com/stopyellingatme/spinup/internal/plugin/rpc"
)
