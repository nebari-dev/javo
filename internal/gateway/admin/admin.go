// Package admin serves the platform admin's console: every person's apps,
// usage, held restores, database backups, the backup bucket, the audit
// log and the AI settings.
package admin

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/ai/aisettings"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/store"
)

// API is the platform admin's operations, all registered through
// authn.RegisterAdmin.
type API struct {
	Workspaces *apps.Workspaces
	DB         *store.Store
	People     authn.People
	Roles      config.Roles
	Now        func() time.Time
	// Backups is where the database backups are.
	Backups BackupConfig
	// Client and Logs read the backup Jobs' pods and their logs.
	Client client.Client
	Logs   podlogs.Source
	// AI is the AI settings in force; nil while AI is off.
	AI *aisettings.Live
}

// Register adds the admin console's routes.
func (a *API) Register(api huma.API) {
	a.registerApps(api)
	a.registerUsage(api)
	a.registerRestores(api)
	a.registerBackups(api)
	a.registerBucket(api)
	a.registerAudit(api)
	a.registerAI(api)
}
