// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package jobs

import (
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

func TestResolveUserJobPermission_Allowed(t *testing.T) {
	cases := []struct {
		name, params, perm string
	}{
		{"container.list", "", identity.PermContainersRead},
		{"container.restart.my-app", "", identity.PermContainersAction},
		{"container.remove.db_1", "", identity.PermContainersAction},
		{"container.logs.web", "", identity.PermContainersRead},
		{"caddy.status", "", identity.PermNetworkRead},
		{"swarm.info", "", identity.PermJobsRun},
		{"vm.list", "", identity.PermVMsRead},
		{"vm.start.ubuntu-1", "", identity.PermVMsAction},
		{"vm.destroy.ubuntu-1", "", identity.PermVMsManage},
		{"docker.install", "", identity.PermNodeUpdate},
		{"app.start", `{"app_name":"nextcloud"}`, identity.PermContainersAction},
		{"app.remove", `{"app_name":"nextcloud"}`, identity.PermContainersDeploy},
		{"backup-db", "", identity.PermJobsRun},
		{"cleanup_logs", "{}", identity.PermJobsRun},
	}
	for _, tc := range cases {
		got, err := ResolveUserJobPermission(tc.name, tc.params)
		if err != nil || got != tc.perm {
			t.Errorf("%s(%q) = %q, %v; want %q", tc.name, tc.params, got, err, tc.perm)
		}
	}
}

func TestResolveUserJobPermission_Rejected(t *testing.T) {
	cases := []struct{ name, params string }{
		// Privileged built-ins that need validated params must use their dedicated endpoints.
		{"swarm.join", `{"manager_addr":"evil:2377","token":"x"}`},
		{"swarm.join", ""},
		{"swarm.leave", ""},
		{"swarm.init", ""},
		{"swarm.service.deploy", `{"image":"evil"}`},
		{"docker.volume.restore", `{"volumes":["/"]}`},
		{"docker.volume.backup", ""},
		{"docker.pg.backup", `{"pg_user":"x; id"}`},
		{"caddy.upsert-route", `{"domain":"x"}`},
		{"script.exec", `{"script_path":"/tmp/x.sh"}`},
		{"marketplace.install", `{"params":"x"}`},
		{"vm.create", `{"name":"x"}`},
		// Params are never accepted outside app.*
		{"container.list", `{"x":1}`},
		{"backup-db", `{"script_path":"/etc/x"}`},
		// Malformed names / smuggling attempts.
		{"container.restart.a;b", ""},
		{"container.exec.web", ""},
		{"vm.start.a.b", ""},
		{"../../bin/sh", ""},
		{"", ""},
		{"x y", ""},
		{"app.start", ""},
		{"app.start", `{"app_name":"../x"}`},
		{"app.start", `{"app_name":"ok","extra":1}`},
		{"app.unknown", `{"app_name":"ok"}`},
	}
	for _, tc := range cases {
		if perm, err := ResolveUserJobPermission(tc.name, tc.params); err == nil {
			t.Errorf("%s(%q) should be rejected, got perm %q", tc.name, tc.params, perm)
		}
	}
}
