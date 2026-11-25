// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/arturo/autohost-cloud-api/internal/mcpserver"
)

// Effects of a tool call. Calls whose effect is not read wait for the user's approval.
const (
	EffectRead        = mcpserver.EffectRead
	EffectWrite       = mcpserver.EffectWrite
	EffectDestructive = mcpserver.EffectDestructive
)

const (
	// maxToolResultChars bounds what a tool result adds to the model's context.
	maxToolResultChars = 6000
	// jobWait bounds how long a tool waits for a node job (container action, logs) to finish.
	jobWait = 30 * time.Second
	// nodeOnlineWindow matches the panel: a node is online if it reported in the last 45 seconds.
	nodeOnlineWindow = 45 * time.Second
)

// Exec calls one API operation as the user (mcpserver.Execute through the API router).
type Exec func(ctx context.Context, operationID string, params map[string]any, body any) (*mcpserver.Result, error)

// toolEnv is what a tool needs to run.
type toolEnv struct {
	exec  Exec
	index *mcpserver.Index
	now   func() time.Time
}

// Tool is a function the model can call.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	// effect is the tool's fixed effect; execute_endpoint computes it per call from the operation.
	effect  string
	run     func(ctx context.Context, env *toolEnv, args map[string]any) (any, error)
	summary func(args map[string]any) string
}

func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

var containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// errToolInput is reported to the model so it can fix its call.
var errToolInput = errors.New("invalid arguments")

func argString(args map[string]any, name string, required bool) (string, error) {
	v, ok := args[name]
	if !ok || v == nil {
		if required {
			return "", fmt.Errorf("%w: %q is required", errToolInput, name)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: %q must be a string", errToolInput, name)
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", fmt.Errorf("%w: %q is required", errToolInput, name)
	}
	return s, nil
}

// apiResult turns an API answer into a tool result: the body on success, an error object otherwise.
func apiResult(res *mcpserver.Result) (any, error) {
	if res.Status >= 400 {
		return map[string]any{"error": res.Body, "http_status": res.Status}, nil
	}
	if res.Body == nil {
		return map[string]any{"ok": true, "http_status": res.Status}, nil
	}
	return res.Body, nil
}

// call runs an operation and returns its tool result.
func (env *toolEnv) call(ctx context.Context, op string, params map[string]any, body any) (any, error) {
	res, err := env.exec(ctx, op, params, body)
	if err != nil {
		return nil, err
	}
	return apiResult(res)
}

// pick keeps only some fields of each object in a list (or of one object), so list results stay small.
func pick(v any, fields ...string) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, pick(item, fields...))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for _, f := range fields {
			if val, ok := t[f]; ok && val != nil && val != "" {
				out[f] = val
			}
		}
		return out
	default:
		return v
	}
}

// runJob dispatches a node command and waits for it to finish, so the model gets the outcome in one call.
func (env *toolEnv) runJob(ctx context.Context, nodeID, command string) (any, error) {
	res, err := env.exec(ctx, "dispatchJob", nil, map[string]any{"node_id": nodeID, "command_name": command})
	if err != nil {
		return nil, err
	}
	if res.Status >= 400 {
		return apiResult(res)
	}
	job, _ := res.Body.(map[string]any)
	jobID, _ := job["id"].(string)
	if jobID == "" {
		return apiResult(res)
	}
	deadline := time.Now().Add(jobWait)
	for {
		status, _ := job["status"].(string)
		if status == "completed" || status == "failed" {
			out := map[string]any{"job_id": jobID, "status": status}
			if o, _ := job["output"].(string); o != "" {
				out["output"] = tail(o, 4000)
			}
			if e, _ := job["error"].(string); e != "" {
				out["error"] = e
			}
			return out, nil
		}
		if time.Now().After(deadline) {
			return map[string]any{"job_id": jobID, "status": status, "note": "still running; the node may be offline or busy"}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		res, err := env.exec(ctx, "getJob", map[string]any{"id": jobID}, nil)
		if err != nil {
			return nil, err
		}
		if res.Status >= 400 {
			return apiResult(res)
		}
		job, _ = res.Body.(map[string]any)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// tools returns the tools offered to the model. The direct tools cover common tasks with small schemas
// (easier for small local models); the three meta-tools reach every other API operation.
func tools() []*Tool {
	nodeID := strProp("node ID (UUID) from list_nodes, or the node's exact hostname")
	container := strProp("container name, as returned by list_containers")
	return []*Tool{
		{
			Name:        "list_nodes",
			Description: "List the user's nodes (servers) with online status, IPs and the latest CPU, memory and disk usage. Use it to find a node ID from its name.",
			Parameters:  obj(map[string]any{}),
			effect:      EffectRead,
			run: func(ctx context.Context, env *toolEnv, _ map[string]any) (any, error) {
				res, err := env.exec(ctx, "listNodesWithMetrics", nil, nil)
				if err != nil {
					return nil, err
				}
				if res.Status >= 400 {
					return apiResult(res)
				}
				nodes, _ := res.Body.([]any)
				out := make([]any, 0, len(nodes))
				for _, n := range nodes {
					m, _ := n.(map[string]any)
					item := pick(m, "id", "hostname", "ip_local", "ip_vpn", "os", "project_id", "last_seen_at").(map[string]any)
					item["online"] = env.isOnline(m["last_seen_at"])
					if metric, ok := m["last_metric"].(map[string]any); ok {
						item["usage"] = pick(metric, "cpu_usage_percent", "memory_usage_percent", "disk_usage_percent", "cpu_temp_celsius")
					}
					out = append(out, item)
				}
				return out, nil
			},
		},
		{
			Name:        "list_containers",
			Description: "List the Docker containers of a node with their state, image, CPU and memory.",
			Parameters:  obj(map[string]any{"node_id": nodeID}, "node_id"),
			effect:      EffectRead,
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, err := env.nodeArg(ctx, args)
				if err != nil {
					return nil, err
				}
				res, err := env.exec(ctx, "listContainers", map[string]any{"nodeId": id}, nil)
				if err != nil {
					return nil, err
				}
				if res.Status >= 400 {
					return apiResult(res)
				}
				body, _ := res.Body.(map[string]any)
				out := map[string]any{"containers": pick(body["containers"], "name", "image", "state", "status", "cpu", "mem")}
				if e, _ := body["error"].(string); e != "" {
					out["error"] = e
				}
				if c, _ := body["cached"].(bool); c {
					out["note"] = "the node did not answer: last known snapshot"
				}
				return out, nil
			},
		},
		{
			Name:        "container_logs",
			Description: "Get the latest logs of a container.",
			Parameters:  obj(map[string]any{"node_id": nodeID, "container": container}, "node_id", "container"),
			effect:      EffectRead,
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, name, err := env.nodeAndContainer(ctx, args)
				if err != nil {
					return nil, err
				}
				return env.runJob(ctx, id, "container.logs."+name)
			},
		},
		{
			Name:        "container_action",
			Description: "Start, stop or restart a container on a node. The user is asked to approve it before it runs.",
			Parameters: obj(map[string]any{
				"node_id":   nodeID,
				"container": container,
				"action":    map[string]any{"type": "string", "enum": []string{"start", "stop", "restart"}},
			}, "node_id", "container", "action"),
			effect: EffectWrite,
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, name, err := env.nodeAndContainer(ctx, args)
				if err != nil {
					return nil, err
				}
				action, _ := argString(args, "action", true)
				if action != "start" && action != "stop" && action != "restart" {
					return nil, fmt.Errorf("%w: action must be start, stop or restart", errToolInput)
				}
				return env.runJob(ctx, id, "container."+action+"."+name)
			},
			summary: func(args map[string]any) string {
				return fmt.Sprintf("%s container %v on node %v", strings.ToUpper(fmt.Sprint(args["action"])), args["container"], args["node_id"])
			},
		},
		{
			Name:        "list_monitors",
			Description: "List the service monitors (HTTP/TCP/process health checks) of a node with their status and last result.",
			Parameters:  obj(map[string]any{"node_id": nodeID}, "node_id"),
			effect:      EffectRead,
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, err := env.nodeArg(ctx, args)
				if err != nil {
					return nil, err
				}
				res, err := env.call(ctx, "listServiceMonitors", map[string]any{"nodeId": id}, nil)
				if err != nil {
					return nil, err
				}
				return pick(res, "id", "service_name", "check_type", "status", "consecutive_failures", "last_message", "last_latency_ms", "last_checked_at"), nil
			},
		},
		{
			Name:        "list_alerts",
			Description: "List recently fired alerts (node offline, high CPU, memory or disk).",
			Parameters:  obj(map[string]any{}),
			effect:      EffectRead,
			run: func(ctx context.Context, env *toolEnv, _ map[string]any) (any, error) {
				res, err := env.call(ctx, "listAlertEvents", nil, nil)
				if err != nil {
					return nil, err
				}
				return pick(res, "node_id", "alert_type", "message", "created_at"), nil
			},
		},
		{
			Name:        "list_backups",
			Description: "List the backups and restores of a container.",
			Parameters:  obj(map[string]any{"node_id": nodeID, "container": container}, "node_id", "container"),
			effect:      EffectRead,
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, name, err := env.nodeAndContainer(ctx, args)
				if err != nil {
					return nil, err
				}
				res, err := env.call(ctx, "listBackupRecords", map[string]any{"nodeId": id, "containerName": name}, nil)
				if err != nil {
					return nil, err
				}
				return pick(res, "id", "status", "trigger_type", "backup_method", "file_size_bytes", "started_at", "finished_at", "error_message"), nil
			},
		},
		{
			Name:        "run_backup",
			Description: "Back up a container now. The user is asked to approve it before it runs.",
			Parameters:  obj(map[string]any{"node_id": nodeID, "container": container}, "node_id", "container"),
			effect:      EffectWrite,
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, name, err := env.nodeAndContainer(ctx, args)
				if err != nil {
					return nil, err
				}
				res, err := env.call(ctx, "runBackup", map[string]any{"nodeId": id, "containerName": name}, nil)
				if err != nil {
					return nil, err
				}
				return pick(res, "id", "status", "job_id", "error"), nil
			},
			summary: func(args map[string]any) string {
				return fmt.Sprintf("Back up container %v on node %v", args["container"], args["node_id"])
			},
		},
		{
			Name:        "list_installed_apps",
			Description: "List the marketplace apps installed on a node.",
			Parameters:  obj(map[string]any{"node_id": nodeID}, "node_id"),
			effect:      EffectRead,
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, err := env.nodeArg(ctx, args)
				if err != nil {
					return nil, err
				}
				return env.call(ctx, "listNodeMarketplaceApps", map[string]any{"nodeID": id}, nil)
			},
		},
		{
			Name:        "search_endpoints",
			Description: "Search the full AutoHost API for an operation not covered by the other tools (projects, connectors, Caddy routes, files, VMs, users...). Returns operation_id, method, path, summary and effect.",
			Parameters: obj(map[string]any{
				"query": strProp("keywords, e.g. 'caddy route' or 'delete backup'"),
				"tag":   strProp("optional tag, e.g. Nodes, Backups, Connectors"),
			}),
			effect: EffectRead,
			run: func(_ context.Context, env *toolEnv, args map[string]any) (any, error) {
				q, _ := argString(args, "query", false)
				tag, _ := argString(args, "tag", false)
				results, total := env.index.Search(q, tag, 10)
				out := make([]any, 0, len(results))
				for _, op := range results {
					item := map[string]any{"operation_id": op.ID, "method": op.Method, "path": op.Path, "summary": op.Summary, "effect": op.Effect}
					if op.Blocked != "" {
						item["not_available"] = op.Blocked
					}
					out = append(out, item)
				}
				return map[string]any{"total": total, "results": out}, nil
			},
		},
		{
			Name:        "get_endpoint_schema",
			Description: "Get the parameters and request body of an API operation found with search_endpoints.",
			Parameters:  obj(map[string]any{"operation_id": strProp("operation_id from search_endpoints")}, "operation_id"),
			effect:      EffectRead,
			run: func(_ context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, err := argString(args, "operation_id", true)
				if err != nil {
					return nil, err
				}
				op, ok := env.index.Operation(id)
				if !ok {
					return nil, fmt.Errorf("%w: unknown operation_id %q, use search_endpoints", errToolInput, id)
				}
				s := env.index.Schema(op)
				out := map[string]any{"operation_id": op.ID, "method": op.Method, "path": op.Path, "effect": op.Effect, "description": s.Description, "parameters": s.Parameters}
				if s.RequestBody != nil {
					out["request_body"] = s.RequestBody
				}
				return out, nil
			},
		},
		{
			Name:        "execute_endpoint",
			Description: "Call an API operation found with search_endpoints. Operations that change something are shown to the user for approval first.",
			Parameters: obj(map[string]any{
				"operation_id": strProp("operation_id from search_endpoints"),
				"params":       map[string]any{"type": "object", "description": "path and query parameters by name"},
				"body":         map[string]any{"type": "object", "description": "JSON request body, if the operation takes one"},
			}, "operation_id"),
			run: func(ctx context.Context, env *toolEnv, args map[string]any) (any, error) {
				id, err := argString(args, "operation_id", true)
				if err != nil {
					return nil, err
				}
				params, _ := args["params"].(map[string]any)
				return env.call(ctx, id, params, args["body"])
			},
			summary: func(args map[string]any) string {
				params, _ := json.Marshal(args["params"])
				s := fmt.Sprintf("%v %s", args["operation_id"], params)
				if args["body"] != nil {
					body, _ := json.Marshal(args["body"])
					s += " body " + string(body)
				}
				return s
			},
		},
	}
}

// resolveNode accepts a node ID or, since small models often pass the name they were told, a hostname
// that matches exactly one visible node.
func (env *toolEnv) resolveNode(ctx context.Context, value string) (string, error) {
	if _, err := uuid.Parse(value); err == nil {
		return value, nil
	}
	res, err := env.exec(ctx, "listNodes", nil, nil)
	if err != nil {
		return "", err
	}
	nodes, _ := res.Body.([]any)
	var matches []string
	for _, n := range nodes {
		m, _ := n.(map[string]any)
		if host, _ := m["hostname"].(string); strings.EqualFold(host, value) {
			id, _ := m["id"].(string)
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("%w: no node is named %q; call list_nodes and use its id", errToolInput, value)
	default:
		return "", fmt.Errorf("%w: several nodes are named %q; call list_nodes and use the id", errToolInput, value)
	}
}

// nodeArg reads node_id and resolves it to an ID.
func (env *toolEnv) nodeArg(ctx context.Context, args map[string]any) (string, error) {
	v, err := argString(args, "node_id", true)
	if err != nil {
		return "", err
	}
	return env.resolveNode(ctx, v)
}

func (env *toolEnv) nodeAndContainer(ctx context.Context, args map[string]any) (string, string, error) {
	// Check the container name first: an invalid call must not reach the API at all.
	name, err := argString(args, "container", true)
	if err != nil {
		return "", "", err
	}
	if !containerNameRe.MatchString(name) {
		return "", "", fmt.Errorf("%w: %q is not a valid container name", errToolInput, name)
	}
	id, err := env.nodeArg(ctx, args)
	if err != nil {
		return "", "", err
	}
	return id, name, nil
}

func (env *toolEnv) isOnline(lastSeen any) bool {
	s, _ := lastSeen.(string)
	t, err := time.Parse(time.RFC3339, s)
	return err == nil && env.now().Sub(t) <= nodeOnlineWindow
}

// effectOf returns what a call does: the tool's effect, or for execute_endpoint the operation's.
func effectOf(t *Tool, args map[string]any, index *mcpserver.Index) string {
	if t.effect != "" {
		return t.effect
	}
	id, _ := args["operation_id"].(string)
	if op, ok := index.Operation(strings.TrimSpace(id)); ok {
		return op.Effect
	}
	return EffectRead // unknown operations fail without doing anything
}

// specs turns tools into the definitions sent to the model; in read-only mode, tools that change
// something are left out (execute_endpoint stays, limited to read operations at run time).
func specs(ts []*Tool, readOnly bool) []toolSpec {
	out := make([]toolSpec, 0, len(ts))
	for _, t := range ts {
		if readOnly && t.effect != "" && t.effect != EffectRead {
			continue
		}
		desc := t.Description
		if readOnly && t.Name == "execute_endpoint" {
			desc = "Call a read-only (GET) API operation found with search_endpoints. Read-only mode: operations that change something are refused."
		}
		out = append(out, toolSpec{Type: "function", Function: functionSpec{Name: t.Name, Description: desc, Parameters: t.Parameters}})
	}
	return out
}

// encodeResult serializes a tool result for the model, bounded to maxToolResultChars.
func encodeResult(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return `{"error":"could not encode the result"}`
	}
	if len(raw) <= maxToolResultChars {
		return string(raw)
	}
	return string(raw[:maxToolResultChars]) + `… [truncated: refine the request to see more]`
}
