// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

// ResolveUserJobPermission decides which commands a user may dispatch directly (POST /v1/jobs, scheduled tasks)
// and which node permission each one requires.
//
// Commands that need structured parameters (backups/restores, swarm orchestration, caddy routes,
// marketplace installs, VM creation, arbitrary script paths) are intentionally NOT dispatchable here:
// they have dedicated endpoints that validate their inputs and authorization. Exposing them through
// the generic jobs endpoint let any user with jobs:run run them with attacker-controlled parameters.

var (
	// safeJobNameRe matches the overall shape of a command name.
	safeJobNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,200}$`)
	// customScriptNameRe matches custom script commands (no dots, so they can never alias a built-in).
	customScriptNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// safeContainerNameRe mirrors the agent's container name validation.
	safeContainerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-.]{0,127}$`)
	// safeVMNameRe mirrors the agent's VM name validation.
	safeVMNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	// safeAppNameRe mirrors the agent's app name validation.
	safeAppNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

// ErrJobNotDispatchable is returned for commands that must go through their dedicated endpoint.
var ErrJobNotDispatchable = errors.New("this command cannot be dispatched directly; use its dedicated endpoint")

// fixedJobPermissions lists parameterless built-in commands and the permission each requires.
var fixedJobPermissions = map[string]string{
	"container.list":   identity.PermContainersRead,
	"caddy.status":     identity.PermNetworkRead,
	"swarm.info":       identity.PermJobsRun,
	"swarm.tasks.list": identity.PermJobsRun,
	"vm.list":          identity.PermVMsRead,
	"docker.install":   identity.PermNodeUpdate,
}

// ResolveUserJobPermission validates a user-dispatched command (and its params) and returns the node
// permission required to run it.
func ResolveUserJobPermission(commandName, params string) (string, error) {
	name := strings.TrimSpace(commandName)
	if !safeJobNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid command name %q", commandName)
	}
	params = strings.TrimSpace(params)

	// app.* commands accept exactly one parameter: the app name.
	if strings.HasPrefix(name, "app.") {
		switch name {
		case "app.start", "app.stop", "app.restart":
			if err := validateAppNameParams(params); err != nil {
				return "", err
			}
			return identity.PermContainersAction, nil
		case "app.remove":
			if err := validateAppNameParams(params); err != nil {
				return "", err
			}
			return identity.PermContainersDeploy, nil
		}
		return "", ErrJobNotDispatchable
	}

	// Every other user-dispatchable command takes no parameters.
	if params != "" && params != "{}" && params != "null" {
		return "", errors.New("params are not accepted for this command")
	}

	if perm, ok := fixedJobPermissions[name]; ok {
		return perm, nil
	}

	if strings.HasPrefix(name, "container.") {
		parts := strings.SplitN(name, ".", 3)
		if len(parts) != 3 || !safeContainerNameRe.MatchString(parts[2]) {
			return "", fmt.Errorf("invalid container command %q", name)
		}
		switch parts[1] {
		case "start", "stop", "restart", "remove":
			return identity.PermContainersAction, nil
		case "stats", "logs":
			return identity.PermContainersRead, nil
		}
		return "", ErrJobNotDispatchable
	}

	if strings.HasPrefix(name, "vm.") {
		parts := strings.SplitN(name, ".", 3)
		if len(parts) != 3 || !safeVMNameRe.MatchString(parts[2]) {
			return "", ErrJobNotDispatchable // includes vm.create, which needs a validated payload
		}
		switch parts[1] {
		case "start", "stop", "reboot":
			return identity.PermVMsAction, nil
		case "destroy":
			return identity.PermVMsManage, nil
		}
		return "", ErrJobNotDispatchable
	}

	// Remaining names are custom scripts registered on the node (run from the agent's commands dir).
	// Dotted names are reserved for built-ins, so anything else with a dot is rejected here.
	if customScriptNameRe.MatchString(name) {
		return identity.PermJobsRun, nil
	}
	return "", ErrJobNotDispatchable
}

func validateAppNameParams(params string) error {
	if params == "" {
		return errors.New("app_name param is required")
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		return errors.New("params must be a JSON object")
	}
	if len(p) != 1 {
		return errors.New("only the app_name param is accepted")
	}
	appName, ok := p["app_name"].(string)
	if !ok || !safeAppNameRe.MatchString(appName) {
		return errors.New("invalid app_name")
	}
	return nil
}
