package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/koanf/v2"
	"github.com/knadh/listmonk/internal/migrations"
	lmstore "github.com/knadh/listmonk/internal/store"
	"github.com/knadh/stuffbin"
	"golang.org/x/mod/semver"
)

// migFunc represents a migration function for a particular version.
// fn (generally) executes database migrations and additionally
// takes the filesystem and config objects in case there are additional bits
// of logic to be performed before executing upgrades. fn is idempotent.
type migFunc struct {
	version string
	fn      func(*sqlx.DB, stuffbin.FileSystem, *koanf.Koanf, *log.Logger) error
}

// migList is the list of available migList ordered by the semver.
// Each migration is a Go file in internal/migrations named after the semver.
// The functions are named as: v0.7.0 => migrations.V0_7_0() and are idempotent.
var migList = []migFunc{
	{"v0.4.0", migrations.V0_4_0},
	{"v0.7.0", migrations.V0_7_0},
	{"v0.8.0", migrations.V0_8_0},
	{"v0.9.0", migrations.V0_9_0},
	{"v1.0.0", migrations.V1_0_0},
	{"v2.0.0", migrations.V2_0_0},
	{"v2.1.0", migrations.V2_1_0},
	{"v2.2.0", migrations.V2_2_0},
	{"v2.3.0", migrations.V2_3_0},
	{"v2.4.0", migrations.V2_4_0},
	{"v2.5.0", migrations.V2_5_0},
	{"v3.0.0", migrations.V3_0_0},
	{"v4.0.0", migrations.V4_0_0},
	{"v4.1.0", migrations.V4_1_0},
	{"v5.0.0", migrations.V5_0_0},
	{"v5.1.0", migrations.V5_1_0},
	{"v6.0.0", migrations.V6_0_0},
	{"v6.1.0", migrations.V6_1_0},
	{"v6.2.0", migrations.V6_2_0},
	{"v7.0.0", migrations.V7_0_0},
}

// upgrade upgrades the database to the current version by running SQL migration files
// for all version from the last known version to the current one.
// If record is false, migration versions are not recorded in the DB (used for non-final builds).
func upgrade(st lmstore.Store, fs stuffbin.FileSystem, prompt bool, record bool) {
	if prompt {
		var ok string
		fmt.Printf("** IMPORTANT: Take a backup of the database before upgrading.\n")
		fmt.Print("continue (y/n)?  ")
		if _, err := fmt.Scanf("%s", &ok); err != nil {
			lo.Fatalf("error reading value from terminal: %v", err)
		}
		if strings.ToLower(ok) != "y" {
			fmt.Println("upgrade cancelled")
			return
		}
	}

	// Execute migrations in succession.
	if err := st.Schema().Migrate(context.Background(), lmstore.MigrationEnv{FS: fs, Config: ko, Log: lo}, record); err != nil {
		lo.Fatalf("%v", err)
	}
}

// checkUpgrade checks if the current database schema matches the expected
// binary version.
func checkUpgrade(st lmstore.Store) {
	ctx := context.Background()
	vers, err := st.Schema().Pending(ctx)
	if err != nil {
		lo.Fatalf("error checking migrations: %v", err)
	}

	// No migrations to run.
	if len(vers) == 0 {
		return
	}

	lastVer, err := st.Schema().Version(ctx)
	if err != nil {
		lo.Fatalf("error checking migrations: %v", err)
	}

	lo.Fatalf(`there are %d pending database upgrade(s): %v. The last upgrade was %s. Backup the database and run listmonk --upgrade`,
		len(vers), vers, lastVer)
}

// isNonFinalBuild checks whether migrations may change between builds (nightly, RC, beta).
// This is used to decide whether to run upgrades automatically on boot and not record
// the non-release versions in the table so that when the actual release is out, the
// migrations are applied correctly + the final version is recorded in the table.
func isNonFinalBuild(version string) bool {
	pre := semver.Prerelease(version)
	return strings.Contains(version, "nightly") || strings.HasPrefix(pre, "-beta") || strings.HasPrefix(pre, "-rc")
}
