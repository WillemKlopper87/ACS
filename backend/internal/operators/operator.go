// Package operators owns human-operator identity and role for cmd/api's
// REST surface (design doc v3 §11.3 credential class 4). It is
// deliberately separate from internal/auth's CPE-facing Digest
// authenticator (a different credential class entirely, v3 §11.3 class
// 1) and from cmd/bssadapter's bearer token (class 5, service-to-service
// — a BSS/CRM system, not a human).
package operators

import "time"

// Roles are ranked; a route's minimum role is satisfied by any role at or
// above it in this ordering (readonly < noc < manager < superadmin).
// Renamed from the original 3-tier readonly/operator/admin (admin-platform
// backlog, migration 0032) — RoleOperator/RoleAdmin are kept as aliases so
// any stray old reference still compiles, but new code should use the
// 4-tier names.
const (
	RoleReadOnly   = "readonly"
	RoleNOC        = "noc"
	RoleManager    = "manager"
	RoleSuperAdmin = "superadmin"

	// Deprecated: use RoleNOC/RoleSuperAdmin.
	RoleOperator = RoleNOC
	RoleAdmin    = RoleSuperAdmin
)

var rank = map[string]int{
	RoleReadOnly:   0,
	RoleNOC:        1,
	RoleManager:    2,
	RoleSuperAdmin: 3,
}

// AllRoles lists every role in ascending rank order — the operator-editor
// UI's dropdown and the role-permissions matrix both iterate this rather
// than hardcoding the list a second time.
var AllRoles = []string{RoleReadOnly, RoleNOC, RoleManager, RoleSuperAdmin}

// Permission keys for the curated set of highest-stakes capabilities
// superadmin can configure per role (migration 0032's role_permissions
// table) — deliberately not one key per REST route (72 of them); routine
// read/list/write routes stay gated by the rank system above. See that
// migration's comment for why this scope was chosen over full per-route
// configurability.
const (
	PermDevicesWrite     = "devices.write" // parameter writes, tags, reboot, factory-reset, add/delete object, schedule-inform, parameter-attributes
	PermConnectionReq    = "connection_request"
	PermDiagnosticsRun   = "diagnostics.run" // ping, traceroute, refresh-cellular/wifi, parameter discovery
	PermFirmwareManage   = "firmware.manage" // upload images, create/start/advance rollouts
	PermTemplateManage   = "template.manage"
	PermPolicyManage     = "policy.manage"
	PermScheduleManage   = "schedule.manage"
	PermGroupManage      = "group.manage"
	PermCredentialManage = "credential.manage" // rotate/activate/revoke device Connection Request credentials
	PermCLIAccess        = "cli.access"        // SSH/Telnet console + web GUI proxy config/connect
	PermBulkActions      = "bulk_actions"
	PermUploadRequest    = "upload.request" // request config backup / log file from a device
	PermTenancyManage    = "tenancy.manage" // assign a device's customer/projects (admin-platform backlog: multi-tenancy) — structural tenancy CRUD (regions/customers/projects themselves, operator scope assignment) stays superadmin-only, not part of this matrix
)

// AllPermissions lists every configurable permission key, in the order the
// matrix UI presents them.
var AllPermissions = []string{
	PermDevicesWrite, PermConnectionReq, PermDiagnosticsRun, PermFirmwareManage,
	PermTemplateManage, PermPolicyManage, PermScheduleManage, PermGroupManage,
	PermCredentialManage, PermCLIAccess, PermBulkActions, PermUploadRequest, PermTenancyManage,
}

// defaultPermissions seeds role_permissions on first migration/bootstrap —
// chosen to match the old 3-tier behavior exactly (manager gets everything
// operator/admin's write routes used to allow short of operator
// management, noc gets what the old "operator" rank could do, readonly
// gets nothing) so upgrading is a no-op for existing deployments until a
// superadmin actually changes something.
var defaultPermissions = map[string]map[string]bool{
	RoleManager: {
		PermDevicesWrite: true, PermConnectionReq: true, PermDiagnosticsRun: true, PermFirmwareManage: true,
		PermTemplateManage: true, PermPolicyManage: true, PermScheduleManage: true, PermGroupManage: true,
		PermCredentialManage: true, PermCLIAccess: true, PermBulkActions: true, PermUploadRequest: true,
		PermTenancyManage: true,
	},
	RoleNOC: {
		PermDevicesWrite: true, PermConnectionReq: true, PermDiagnosticsRun: true, PermFirmwareManage: false,
		PermTemplateManage: false, PermPolicyManage: false, PermScheduleManage: false, PermGroupManage: false,
		PermCredentialManage: false, PermCLIAccess: true, PermBulkActions: true, PermUploadRequest: true,
		PermTenancyManage: false,
	},
	RoleReadOnly: {
		PermDevicesWrite: false, PermConnectionReq: false, PermDiagnosticsRun: false, PermFirmwareManage: false,
		PermTemplateManage: false, PermPolicyManage: false, PermScheduleManage: false, PermGroupManage: false,
		PermCredentialManage: false, PermCLIAccess: false, PermBulkActions: false, PermUploadRequest: false,
		PermTenancyManage: false,
	},
}

// ValidRole reports whether role is one of the three known roles.
func ValidRole(role string) bool {
	_, ok := rank[role]
	return ok
}

// AtLeast reports whether role satisfies a route's minimum required role.
// An unrecognized role never satisfies anything.
func AtLeast(role, min string) bool {
	r, ok := rank[role]
	if !ok {
		return false
	}
	m, ok := rank[min]
	if !ok {
		return false
	}
	return r >= m
}

// Operator is a row of the operators table.
type Operator struct {
	ID           string
	Username     string
	Email        string // optional — required only for self-service email password reset
	PasswordHash string
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// TokenVersion is stamped into every JWT issued for this operator;
	// a token whose version is behind this value is revoked.
	TokenVersion int
}
