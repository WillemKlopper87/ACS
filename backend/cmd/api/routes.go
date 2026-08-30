package main

import (
	"database/sql"
	"net/http"

	"acs/internal/observability"
	"acs/internal/operators"
)

// registerRoutes builds the operator API's route table — every REST
// endpoint, its minimum role or permission, and its metrics wrapper — on
// a fresh mux. Extracted from main() (audit P3.1) so tests can construct
// the real router against a test database (cmd/api/integration_test.go)
// and so the OpenAPI drift test has one file to read.
func (h *handler) registerRoutes(metrics *observability.Metrics, db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()
	// route(method, pattern, minRole, fn) wraps a handler with both HTTP
	// metrics (build plan §4 Phase 7) and role enforcement, in one place
	// so no registration below can accidentally skip either.
	ro, op, admin := operators.RoleReadOnly, operators.RoleOperator, operators.RoleAdmin
	route := func(method, pattern, minRole string, fn http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, metrics.InstrumentHTTP(method+" "+pattern, h.requireRole(minRole, fn)))
	}
	// routePerm is route()'s counterpart for the curated capability list
	// (migration 0032) — a superadmin-configurable permission check instead
	// of a fixed rank minimum. Still requires at least noc rank first (via
	// requireRole) so a bare "readonly" JWT never reaches the permission
	// check at all — readonly has no permissions by definition, but this
	// keeps that invariant enforced in two independent places rather than
	// relying solely on the seeded-false defaults.
	routePerm := func(method, pattern, perm string, fn http.HandlerFunc) {
		gated := h.requirePermission(perm, fn)
		mux.HandleFunc(method+" "+pattern, metrics.InstrumentHTTP(method+" "+pattern, h.requireRole(op, gated)))
	}
	mux.HandleFunc("GET /metrics", metrics.Handler().ServeHTTP)
	mux.Handle("GET /healthz", observability.LivenessHandler())
	mux.Handle("GET /readyz", observability.ReadinessHandler(db))
	mux.HandleFunc("POST /api/v1/auth/login", metrics.InstrumentHTTP("POST /api/v1/auth/login", h.login))
	route("POST", "/api/v1/auth/ticket", ro, h.issueBrowserTicket) // audit P1.4 — see issueBrowserTicket
	route("POST", "/api/v1/auth/logout", ro, h.logout)             // revokes every session of the caller
	route("POST", "/api/v1/auth/operators", admin, h.createOperator)
	route("GET", "/api/v1/auth/operators", admin, h.listOperators)
	route("PUT", "/api/v1/auth/operators/{id}/password", admin, h.resetOperatorPassword)
	route("GET", "/api/v1/auth/role-permissions", admin, h.getRolePermissions)
	route("PUT", "/api/v1/auth/role-permissions", admin, h.setRolePermission)
	mux.HandleFunc("POST /api/v1/auth/password-reset/request", metrics.InstrumentHTTP("POST /api/v1/auth/password-reset/request", h.requestPasswordReset))
	mux.HandleFunc("POST /api/v1/auth/password-reset/confirm", metrics.InstrumentHTTP("POST /api/v1/auth/password-reset/confirm", h.confirmPasswordReset))
	route("POST", "/api/v1/auth/operators/{id}/scopes", admin, h.setOperatorScopes)
	route("GET", "/api/v1/auth/operators/{id}/scopes", admin, h.getOperatorScopes)

	// Multi-tenancy (admin-platform backlog): structural CRUD is
	// superadmin-only (the org chart); assigning a device to a
	// customer/projects is the curated tenancy.manage permission instead.
	route("POST", "/api/v1/regions", admin, h.createRegion)
	route("GET", "/api/v1/regions", ro, h.listRegions)
	route("DELETE", "/api/v1/regions/{id}", admin, h.deleteRegion)
	route("POST", "/api/v1/customers", admin, h.createCustomer)
	route("GET", "/api/v1/customers", ro, h.listCustomers)
	route("DELETE", "/api/v1/customers/{id}", admin, h.deleteCustomer)
	route("POST", "/api/v1/projects", admin, h.createProject)
	route("GET", "/api/v1/projects", ro, h.listProjects)
	route("DELETE", "/api/v1/projects/{id}", admin, h.deleteProject)
	routePerm("PUT", "/api/v1/devices/{id}/customer", operators.PermTenancyManage, h.assignDeviceCustomer)
	routePerm("PUT", "/api/v1/devices/{id}/projects", operators.PermTenancyManage, h.setDeviceProjects)
	route("GET", "/api/v1/devices/{id}/projects", ro, h.getDeviceProjects)
	routePerm("POST", "/api/v1/devices/import", operators.PermTenancyManage, h.importDevices)
	route("GET", "/api/v1/devices", ro, h.listDevices)
	route("GET", "/api/v1/devices/summary", ro, h.listDevicesSummary)
	route("GET", "/api/v1/devices/ids", ro, h.listMatchingDeviceIDs)
	route("GET", "/api/v1/fleet-health", ro, h.fleetHealth)
	route("GET", "/api/v1/dashboard", ro, h.getDashboard)
	route("GET", "/api/v1/dashboard/layout", ro, h.getDashboardLayout)
	route("PUT", "/api/v1/dashboard/layout", ro, h.setDashboardLayout)
	routePerm("POST", "/api/v1/devices/bulk-actions", operators.PermBulkActions, h.bulkAction)
	route("GET", "/api/v1/devices/{id}", ro, h.getDevice)
	route("GET", "/api/v1/devices/{id}/parameters", ro, h.getParameters)
	route("GET", "/api/v1/devices/{id}/parameters/history", ro, h.getParameterHistory)
	routePerm("PUT", "/api/v1/devices/{id}/parameters", operators.PermDevicesWrite, h.putParameters)
	routePerm("POST", "/api/v1/devices/{id}/parameters/get", operators.PermDevicesWrite, h.createGetParametersLive)
	routePerm("PUT", "/api/v1/devices/{id}/tags", operators.PermDevicesWrite, h.updateDeviceTags)
	routePerm("PUT", "/api/v1/devices/{id}/location", operators.PermDevicesWrite, h.updateDeviceLocation)
	route("GET", "/api/v1/reports/devices/export", ro, h.exportDevicesExcel)

	// BSS integration admin panel (admin-platform backlog item #2):
	// superadmin-only, same posture as the Tenancy structural CRUD above.
	route("GET", "/api/v1/bss/mappings", admin, h.listBSSMappings)
	route("POST", "/api/v1/bss/mappings", admin, h.createBSSMapping)
	route("GET", "/api/v1/bss/oauth-clients", admin, h.listBSSOAuthClients)
	route("POST", "/api/v1/bss/oauth-clients", admin, h.createBSSOAuthClient)
	route("DELETE", "/api/v1/bss/oauth-clients/{id}", admin, h.revokeBSSOAuthClient)
	route("GET", "/api/v1/bss/webhooks", admin, h.listBSSWebhooks)
	route("POST", "/api/v1/bss/webhooks", admin, h.createBSSWebhook)
	route("DELETE", "/api/v1/bss/webhooks/{id}", admin, h.deleteBSSWebhook)
	route("GET", "/api/v1/bss/stats", admin, h.getBSSStats)
	route("GET", "/api/v1/bss/health", admin, h.getBSSHealth)
	route("POST", "/api/v1/bss/troubleshoot/mapping-lookup", admin, h.troubleshootMappingLookup)
	route("POST", "/api/v1/bss/troubleshoot/auth-check", admin, h.troubleshootAuthCheck)
	route("POST", "/api/v1/bss/troubleshoot/job-status", admin, h.troubleshootJobStatus)
	route("POST", "/api/v1/bss/troubleshoot/order-dispatch", admin, h.troubleshootOrderDispatch)
	routePerm("POST", "/api/v1/device-groups", operators.PermGroupManage, h.createDeviceGroup)
	route("GET", "/api/v1/device-groups", ro, h.listDeviceGroups)
	route("GET", "/api/v1/device-groups/{id}", ro, h.getDeviceGroup)
	routePerm("DELETE", "/api/v1/device-groups/{id}", operators.PermGroupManage, h.deleteDeviceGroup)
	routePerm("POST", "/api/v1/device-groups/{id}/members", operators.PermGroupManage, h.addDeviceGroupMembers)
	routePerm("DELETE", "/api/v1/device-groups/{id}/members/{device_id}", operators.PermGroupManage, h.removeDeviceGroupMember)
	routePerm("POST", "/api/v1/devices/{id}/credentials/rotate", operators.PermCredentialManage, h.rotateDeviceCredential)
	route("GET", "/api/v1/devices/{id}/credentials", ro, h.listDeviceCredentials)
	routePerm("POST", "/api/v1/devices/{id}/credentials/{credential_id}/activate", operators.PermCredentialManage, h.activateDeviceCredential)
	routePerm("POST", "/api/v1/devices/{id}/credentials/{credential_id}/revoke", operators.PermCredentialManage, h.revokeDeviceCredential)
	routePerm("POST", "/api/v1/scheduled-jobs", operators.PermScheduleManage, h.createScheduledJob)
	route("GET", "/api/v1/scheduled-jobs", ro, h.listScheduledJobs)
	routePerm("DELETE", "/api/v1/scheduled-jobs/{id}", operators.PermScheduleManage, h.deleteScheduledJob)
	routePerm("POST", "/api/v1/scheduled-jobs/{id}/enable", operators.PermScheduleManage, h.setScheduledJobEnabled(true))
	routePerm("POST", "/api/v1/scheduled-jobs/{id}/disable", operators.PermScheduleManage, h.setScheduledJobEnabled(false))
	routePerm("POST", "/api/v1/firmware/rollouts", operators.PermFirmwareManage, h.createRollout)
	route("GET", "/api/v1/firmware/rollouts", ro, h.listRollouts)
	route("GET", "/api/v1/firmware/rollouts/{id}", ro, h.getRollout)
	routePerm("POST", "/api/v1/firmware/rollouts/{id}/start", operators.PermFirmwareManage, h.startRollout)
	routePerm("POST", "/api/v1/firmware/rollouts/{id}/advance", operators.PermFirmwareManage, h.advanceRollout)
	routePerm("POST", "/api/v1/config-templates", operators.PermTemplateManage, h.createTemplate)
	route("GET", "/api/v1/config-templates", ro, h.listTemplates)
	routePerm("DELETE", "/api/v1/config-templates/{id}", operators.PermTemplateManage, h.deleteTemplate)
	routePerm("POST", "/api/v1/config-templates/{id}/apply", operators.PermTemplateManage, h.applyTemplate)
	routePerm("POST", "/api/v1/policies", operators.PermPolicyManage, h.createPolicy)
	route("GET", "/api/v1/policies", ro, h.listPolicies)
	routePerm("DELETE", "/api/v1/policies/{id}", operators.PermPolicyManage, h.deletePolicy)
	routePerm("POST", "/api/v1/policies/{id}/enable", operators.PermPolicyManage, h.setPolicyEnabled(true))
	routePerm("POST", "/api/v1/policies/{id}/disable", operators.PermPolicyManage, h.setPolicyEnabled(false))
	route("GET", "/api/v1/audit-log", ro, h.listAuditLog)
	routePerm("POST", "/api/v1/devices/{id}/parameters/refresh-cellular", operators.PermDiagnosticsRun, h.refreshCellularDiagnostics)
	routePerm("POST", "/api/v1/devices/{id}/parameters/refresh-wifi-clients", operators.PermDiagnosticsRun, h.refreshWifiClients)
	routePerm("POST", "/api/v1/devices/{id}/diagnostics/ping", operators.PermDiagnosticsRun, h.createDiagnosticsPing)
	routePerm("POST", "/api/v1/devices/{id}/diagnostics/traceroute", operators.PermDiagnosticsRun, h.createDiagnosticsTraceroute)
	routePerm("POST", "/api/v1/devices/{id}/objects", operators.PermDevicesWrite, h.createAddObject)
	routePerm("POST", "/api/v1/devices/{id}/objects/delete", operators.PermDevicesWrite, h.createDeleteObject)
	routePerm("POST", "/api/v1/devices/{id}/reboot", operators.PermDevicesWrite, h.createReboot)
	routePerm("POST", "/api/v1/devices/{id}/factory-reset", operators.PermDevicesWrite, h.createFactoryReset)
	routePerm("POST", "/api/v1/devices/{id}/schedule-inform", operators.PermDevicesWrite, h.createScheduleInform)
	routePerm("POST", "/api/v1/devices/{id}/parameter-attributes", operators.PermDevicesWrite, h.createSetParameterAttributes)
	routePerm("POST", "/api/v1/devices/{id}/parameter-attributes/get", operators.PermDevicesWrite, h.createGetParameterAttributes)
	routePerm("POST", "/api/v1/devices/{id}/connection-request", operators.PermConnectionReq, h.createConnectionRequest)
	routePerm("POST", "/api/v1/devices/{id}/discover-parameters", operators.PermDiagnosticsRun, h.createParameterDiscovery)
	route("GET", "/api/v1/devices/{id}/parameter-names", ro, h.getParameterNames)
	routePerm("POST", "/api/v1/devices/{id}/cli/credentials", operators.PermCLIAccess, h.createCLICredential)
	route("GET", "/api/v1/devices/{id}/cli/credentials", ro, h.listCLICredentials)
	routePerm("DELETE", "/api/v1/devices/{id}/cli/credentials/{credential_id}", operators.PermCLIAccess, h.deleteCLICredential)

	// VPN/tunnel concentrator (admin-platform backlog, deliberately last —
	// see internal/vpn's doc comment for scope). Same permission as CLI
	// access: a VPN peer's private key is the same class of remote-access
	// credential material.
	routePerm("POST", "/api/v1/devices/{id}/vpn/enroll", operators.PermCLIAccess, h.enrollVPNPeer)
	routePerm("GET", "/api/v1/devices/{id}/vpn/config", operators.PermCLIAccess, h.getVPNPeerConfig)
	route("GET", "/api/v1/vpn/peers", ro, h.listVPNPeers)
	routePerm("DELETE", "/api/v1/vpn/peers/{peer_id}", operators.PermCLIAccess, h.revokeVPNPeer)
	route("GET", "/api/v1/vpn/concentrator", ro, h.getVPNConcentrator)
	// Not wrapped by route()/metrics.InstrumentHTTP: this is a long-lived
	// WebSocket, not a request/response call, and InstrumentHTTP's
	// duration histogram would just accumulate one enormous bucket for as
	// long as the terminal session stays open. requireRole still applies —
	// this is real device shell access, gated at least as tightly as
	// everything else an operator role can do.
	mux.HandleFunc("GET /api/v1/devices/{id}/cli/connect", h.requireRole(op, h.requirePermission(operators.PermCLIAccess, h.connectCLI)))
	routePerm("PUT", "/api/v1/devices/{id}/webgui", operators.PermCLIAccess, h.setWebGUIConfig)
	route("GET", "/api/v1/devices/{id}/webgui", ro, h.getWebGUIConfig)
	routePerm("DELETE", "/api/v1/devices/{id}/webgui", operators.PermCLIAccess, h.deleteWebGUIConfig)
	// Not wrapped by route() — see proxyWebGUI's doc comment. No method
	// prefix: a device's own admin UI needs POST (forms/settings changes)
	// as much as GET (page loads/assets), not just the latter. Gated by
	// the same permission as configuring it — this is a write-capable
	// channel to the device (its own UI's forms), not a read-only view.
	mux.HandleFunc("/api/v1/devices/{id}/webgui/proxy/{path...}", h.requireRole(op, h.requirePermission(operators.PermCLIAccess, h.proxyWebGUI)))
	routePerm("POST", "/api/v1/devices/{id}/firmware", operators.PermFirmwareManage, h.createFirmwareDownload)
	route("GET", "/api/v1/devices/{id}/jobs", ro, h.listDeviceJobs)
	route("GET", "/api/v1/jobs", ro, h.listJobs)
	route("GET", "/api/v1/jobs/{command_key}", ro, h.getJob)
	route("GET", "/api/v1/firmware/images", ro, h.listFirmwareImages)
	routePerm("POST", "/api/v1/firmware/images", operators.PermFirmwareManage, h.uploadFirmwareImage)
	mux.HandleFunc("GET /api/v1/firmware/images/{id}/file", h.serveFirmwareFile) // CPE traffic, not an operator — see isPublicRoute, uninstrumented to avoid a per-device-download label blowup
	routePerm("POST", "/api/v1/devices/{id}/uploads", operators.PermUploadRequest, h.createDeviceUpload)
	route("GET", "/api/v1/devices/{id}/uploads", ro, h.listDeviceUploads)
	route("GET", "/api/v1/uploads/{id}/file", ro, h.serveUploadedFile)
	mux.HandleFunc("PUT /api/v1/uploads/{id}/receive", metrics.InstrumentHTTP("PUT /api/v1/uploads/{id}/receive", h.receiveUpload)) // CPE traffic — see isPublicRoute

	return mux
}
