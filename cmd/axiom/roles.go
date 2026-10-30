package main

import (
	"fmt"
	"slices"
	"strings"
)

// nodeRole is one job a node takes. Every node runs the same program; the roles in AXIOM_NODE_ROLES decide which
// listeners it opens and which of its stateful parts it starts (docs/architecture.md「部署拓扑」).
type nodeRole string

const (
	// roleCore holds the state: the database, the key ring, the queues and the admin listener. Only a core node serves
	// calls itself.
	roleCore nodeRole = "core"
	// roleIngress terminates clients' connections and forwards every request to a core node over mutual TLS. It holds
	// no state of its own.
	roleIngress nodeRole = "ingress"
	// roleOps runs the operator and monitoring side. Its operations interface is not built yet, so it serves only its
	// probes.
	roleOps nodeRole = "ops"
)

// nodeRoles is the set a node was configured with. Order and repetition carry no meaning; parseRoles enforces that.
type nodeRoles []nodeRole

// parseRoles reads the comma-separated set, refusing anything that is not a known role or appears twice.
func parseRoles(value string) (nodeRoles, error) {
	var roles nodeRoles
	for part := range strings.SplitSeq(value, ",") {
		role := nodeRole(strings.TrimSpace(part))
		if !slices.Contains([]nodeRole{roleCore, roleIngress, roleOps}, role) {
			return nil, fmt.Errorf("%s: want core, ingress or ops, separated by commas", nodeRolesVar)
		}
		if slices.Contains(roles, role) {
			return nil, fmt.Errorf("%s: %s appears twice", nodeRolesVar, role)
		}
		roles = append(roles, role)
	}
	return roles, nil
}

func (r nodeRoles) has(role nodeRole) bool { return slices.Contains(r, role) }

func (r nodeRoles) String() string {
	names := make([]string, len(r))
	for i, role := range r {
		names[i] = string(role)
	}
	return strings.Join(names, ",")
}
