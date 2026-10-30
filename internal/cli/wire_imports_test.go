package cli

// This file gives the cli tests the same command and generator set as the
// binary. The blank imports must match cmd/spinup/wire_gen.go. The test
// TestBlankImportsMatchWireGen fails when they differ.

import (
	_ "github.com/stopyellingatme/spinup/internal/backup"
	_ "github.com/stopyellingatme/spinup/internal/backup/pgdump"
	_ "github.com/stopyellingatme/spinup/internal/backup/walg"
	_ "github.com/stopyellingatme/spinup/internal/doctor/checks"
	_ "github.com/stopyellingatme/spinup/internal/doctor/clientdrift"
	_ "github.com/stopyellingatme/spinup/internal/doctor/composelint"
	_ "github.com/stopyellingatme/spinup/internal/doctor/plainsecrets"
	_ "github.com/stopyellingatme/spinup/internal/doctor/quick"
	_ "github.com/stopyellingatme/spinup/internal/doctor/remote"
	_ "github.com/stopyellingatme/spinup/internal/doctor/services"
	_ "github.com/stopyellingatme/spinup/internal/gen/alert"
	_ "github.com/stopyellingatme/spinup/internal/gen/client"
	_ "github.com/stopyellingatme/spinup/internal/gen/component"
	_ "github.com/stopyellingatme/spinup/internal/gen/dashboard"
	_ "github.com/stopyellingatme/spinup/internal/gen/handler"
	_ "github.com/stopyellingatme/spinup/internal/gen/job"
	_ "github.com/stopyellingatme/spinup/internal/gen/mailer"
	_ "github.com/stopyellingatme/spinup/internal/gen/migration"
	_ "github.com/stopyellingatme/spinup/internal/gen/model"
	_ "github.com/stopyellingatme/spinup/internal/gen/openapi"
	_ "github.com/stopyellingatme/spinup/internal/gen/page"
	_ "github.com/stopyellingatme/spinup/internal/gen/resource"
	_ "github.com/stopyellingatme/spinup/internal/gen/service"
	_ "github.com/stopyellingatme/spinup/internal/gen/serviceclient"
	_ "github.com/stopyellingatme/spinup/internal/maintain"
	_ "github.com/stopyellingatme/spinup/internal/maintain/pg"
	_ "github.com/stopyellingatme/spinup/internal/migrate"
	_ "github.com/stopyellingatme/spinup/internal/monitor"
	_ "github.com/stopyellingatme/spinup/internal/plugin/pack"
	_ "github.com/stopyellingatme/spinup/internal/plugin/render"
	_ "github.com/stopyellingatme/spinup/internal/plugin/rpc"
	_ "github.com/stopyellingatme/spinup/internal/server"
)
