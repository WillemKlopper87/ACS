// Command api is the REST API (build plan §4). Phase 1 added the
// read-only device endpoints. Phase 2 added the first write endpoint —
// PUT parameters queues a SET_PARAMETER job and returns 202 Accepted
// (design doc v3 §8, "all write endpoints ... return 202") — plus reading
// the parameter cache and job status. Phase 3 adds Connection Request:
// unlike SET_PARAMETER/GET_PARAMETER, that job type isn't triggered by an
// inbound CWMP request, so this process also runs a small background
// worker (connreq_worker.go) that goes looking for queued
// CONNECTION_REQUEST jobs instead of waiting for one to be dispatched to
// it.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"acs/internal/bss"
	"acs/internal/cliaccess"
	"acs/internal/config"
	"acs/internal/credentials"
	"acs/internal/dashboard"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/firmware"
	"acs/internal/jobs"
	"acs/internal/mailer"
	"acs/internal/netguard"
	"acs/internal/observability"
	"acs/internal/operators"
	"acs/internal/parameters"
	"acs/internal/policy"
	"acs/internal/ratelimit"
	"acs/internal/rollout"
	"acs/internal/scheduler"
	"acs/internal/store"
	"acs/internal/templates"
	"acs/internal/tenancy"
	"acs/internal/transfer"
	"acs/internal/uploads"
	"acs/internal/vpn"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dsn := os.Getenv("ACS_POSTGRES_DSN")
	if dsn == "" {
		logger.Error("ACS_POSTGRES_DSN is required")
		os.Exit(1)
	}

	// Fail-closed secret enforcement (audit P0.1): without these, the
	// API used to run wide open with only a warning. That mode now
	// requires the explicit ACS_INSECURE_DEV_MODE=true escape hatch.
	apiSecrets := []config.Secret{
		{Env: "ACS_JWT_SIGNING_SECRET", MinBytes: 32, Purpose: "signs operator JWTs; without it every request is anonymous"},
		{Env: "ACS_CREDENTIAL_ENCRYPTION_KEY", MinBytes: 16, Purpose: "encrypts device/CLI/VPN credentials at rest; without it they are stored in plaintext"},
		{Env: "ACS_INTERNAL_SERVICE_TOKEN", MinBytes: 32, Purpose: "authenticates cmd/bssadapter's machine-to-machine calls into this API"},
		{Env: "ACS_BOOTSTRAP_ADMIN_PASSWORD", MinBytes: 12, Purpose: "seeds the first superadmin account", Optional: true},
		{Env: "ACS_BSS_API_TOKEN", MinBytes: 16, Purpose: "authenticates this API's troubleshooting calls to cmd/bssadapter", Optional: true},
	}
	if err := config.Validate(logger, apiSecrets...); err != nil {
		logger.Error("refusing to start", "err", err)
		os.Exit(1)
	}
	config.LogSummary(logger, apiSecrets...)

	// Process lifecycle (audit P1.2): SIGINT/SIGTERM cancels this context,
	// which stops both background workers, then the HTTP server drains.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, dsn)
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	firmwareRoot := envOr("ACS_FIRMWARE_STORAGE_ROOT", "./firmware-storage")
	firmwareStorage, err := firmware.NewStorage(firmwareRoot)
	if err != nil {
		logger.Error("failed to initialize firmware storage", "err", err, "root", firmwareRoot)
		os.Exit(1)
	}

	uploadRoot := envOr("ACS_UPLOAD_STORAGE_ROOT", "./upload-storage")
	uploadStorage, err := uploads.NewStorage(uploadRoot)
	if err != nil {
		logger.Error("failed to initialize upload storage", "err", err, "root", uploadRoot)
		os.Exit(1)
	}

	jwtSecret := []byte(os.Getenv("ACS_JWT_SIGNING_SECRET"))
	if len(jwtSecret) == 0 {
		logger.Warn("ACS_JWT_SIGNING_SECRET not set — cmd/api is running WITHOUT operator authentication (design doc v3 §11.3/§11.4). Every request is treated as an anonymous \"operator\".")
	}

	internalServiceToken := os.Getenv("ACS_INTERNAL_SERVICE_TOKEN")
	if len(jwtSecret) > 0 && internalServiceToken == "" {
		logger.Warn("ACS_INTERNAL_SERVICE_TOKEN not set — cmd/bssadapter's own calls into this API (order dispatch, job status) will get 401'd once JWT auth is enabled, since it has no operator login. Set the same value here and on cmd/bssadapter.")
	}

	credentialsRepo, err := credentials.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		logger.Error("failed to initialize credential encryption", "err", err)
		os.Exit(1)
	}
	if !credentialsRepo.Encrypted() {
		logger.Warn("ACS_CREDENTIAL_ENCRYPTION_KEY not set — device_credentials.password is stored in Postgres in plaintext (critical feature backlog item). Set it to any passphrase to encrypt at rest going forward; existing plaintext rows keep working unmigrated.")
	}

	cliRepo, err := cliaccess.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		logger.Error("failed to initialize cli credential encryption", "err", err)
		os.Exit(1)
	}

	overlaySubnetCIDR := envOr("ACS_VPN_OVERLAY_SUBNET", "10.99.0.0/16")
	_, overlaySubnet, err := net.ParseCIDR(overlaySubnetCIDR)
	if err != nil {
		logger.Error("invalid ACS_VPN_OVERLAY_SUBNET", "err", err, "value", overlaySubnetCIDR)
		os.Exit(1)
	}
	vpnRepo, err := vpn.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"), overlaySubnet)
	if err != nil {
		logger.Error("failed to initialize vpn peer encryption", "err", err)
		os.Exit(1)
	}
	vpnConcentrator := vpn.ConcentratorConfig{
		ServerPublicKey: os.Getenv("ACS_VPN_SERVER_PUBLIC_KEY"),
		Endpoint:        os.Getenv("ACS_VPN_SERVER_ENDPOINT"),
		OverlaySubnet:   overlaySubnetCIDR,
	}
	if !vpnConcentrator.Configured() {
		logger.Warn("ACS_VPN_SERVER_PUBLIC_KEY/ACS_VPN_SERVER_ENDPOINT not set — VPN peers can still be enrolled (keypair + overlay IP allocation both work), but the generated client config will have an empty [Peer] section until a real concentrator host's public key/endpoint are configured. Deliberately last item in the admin-platform backlog — see internal/vpn's doc comment for the full scope.")
	}

	metrics := observability.NewMetrics("api")
	metrics.ObserveDB(db)

	var transferKey []byte
	if len(jwtSecret) > 0 {
		transferKey = transfer.DeriveKey(jwtSecret)
	}
	uploadMaxBytes := int64(256 << 20) // 256 MiB default ceiling per CPE upload
	if v := os.Getenv("ACS_UPLOAD_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			logger.Error("invalid ACS_UPLOAD_MAX_BYTES", "value", v)
			os.Exit(1)
		}
		uploadMaxBytes = n
	}

	// Outbound device-network policy for the web-GUI proxy and console
	// bridge (audit P0.4). Loopback/link-local/metadata/multicast targets
	// are always refused; setting ACS_DEVICE_NET_ALLOWED_CIDRS (comma-
	// separated, e.g. the VPN overlay plus the CPE management subnets)
	// additionally restricts targets to those networks — recommended for
	// any production deployment.
	netPolicy := netguard.Policy{}
	if v := os.Getenv("ACS_DEVICE_NET_ALLOWED_CIDRS"); v != "" {
		cidrs, err := netguard.ParseCIDRList(v)
		if err != nil {
			logger.Error("invalid ACS_DEVICE_NET_ALLOWED_CIDRS", "err", err)
			os.Exit(1)
		}
		netPolicy.AllowedCIDRs = cidrs
	} else {
		logger.Warn("ACS_DEVICE_NET_ALLOWED_CIDRS not set — the web-GUI proxy and console bridge may dial any non-loopback/non-metadata address the ACS host can reach. Set it to your device networks to close this down.")
	}

	h := &handler{
		logger:       logger,
		devices:      devices.NewRepository(db),
		jobs:         jobs.NewRepository(db),
		params:       parameters.NewRepository(db),
		vendors:      adapters.NewRegistry(),
		auditor:      observability.NewAuditor(db),
		firmware:     firmware.NewRepository(db),
		firmwareFS:   firmwareStorage,
		firmwareBase: envOr("ACS_FIRMWARE_BASE_URL", "http://localhost:8080"),
		operators:    operators.NewRepository(db),
		jwtSecret:    jwtSecret,
		transferKey:    transferKey,
		uploadMaxBytes: uploadMaxBytes,
		netPolicy:      netPolicy,
		metrics:      metrics,
		groups:       devices.NewGroupRepository(db),
		credentials:  credentialsRepo,
		schedules:    scheduler.NewRepository(db),
		rollouts:     rollout.NewRepository(db),
		policies:     policy.NewRepository(db),
		uploads:      uploads.NewRepository(db),
		uploadsFS:    uploadStorage,
		uploadsBase:  envOr("ACS_UPLOAD_BASE_URL", "http://localhost:8080"),
		templates:    templates.NewRepository(db),
		cli:          cliRepo,
		permissions:  operators.NewPermissionRepository(db),
		mailer: mailer.New(mailer.Config{
			Host: os.Getenv("ACS_SMTP_HOST"), Port: envOr("ACS_SMTP_PORT", "587"),
			Username: os.Getenv("ACS_SMTP_USERNAME"), Password: os.Getenv("ACS_SMTP_PASSWORD"),
			From: os.Getenv("ACS_SMTP_FROM"),
		}, logger),
		frontendBaseURL: envOr("ACS_FRONTEND_BASE_URL", "http://localhost:5173"),
		tenancy:         tenancy.NewRepository(db),
		dashboards:      dashboard.NewRepository(db),

		bssMappings:     bss.NewRepository(db),
		bssWebhooks:     bss.NewWebhookRepository(db),
		bssOAuthClients: bss.NewOAuthRepository(db),
		bssAdapterURL:   envOr("ACS_BSS_ADAPTER_URL", "http://localhost:8090"),
		bssToken:        os.Getenv("ACS_BSS_API_TOKEN"),
		bssHTTPClient:   &http.Client{Timeout: 10 * time.Second},

		vpnPeers:        vpnRepo,
		vpnConcentrator: vpnConcentrator,
	}
	if h.bssToken == "" {
		logger.Warn("ACS_BSS_API_TOKEN not set — BSS admin-panel troubleshooting calls will hit the adapter unauthenticated (fine only if cmd/bssadapter also has no ACS_BSS_API_TOKEN set)")
	}

	if !h.mailer.Configured() {
		logger.Warn("ACS_SMTP_HOST not set — self-service password reset emails will be logged instead of sent (fine for development; set ACS_SMTP_HOST/PORT/USERNAME/PASSWORD/FROM for real delivery).")
	}

	if err := bootstrapAdmin(ctx, h, os.Getenv("ACS_BOOTSTRAP_ADMIN_USERNAME"), os.Getenv("ACS_BOOTSTRAP_ADMIN_PASSWORD")); err != nil {
		logger.Error("failed to bootstrap admin operator", "err", err)
		os.Exit(1)
	}

	// route(method, pattern, minRole, fn) wraps a handler with both HTTP
	// metrics (build plan §4 Phase 7) and role enforcement, in one place
	// so no registration below can accidentally skip either.
	ro, op, admin := operators.RoleReadOnly, operators.RoleOperator, operators.RoleAdmin
	mux := http.NewServeMux()
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
	mux.HandleFunc("POST /api/v1/auth/login", metrics.InstrumentHTTP("POST /api/v1/auth/login", h.login))
	route("POST", "/api/v1/auth/ticket", ro, h.issueBrowserTicket) // audit P1.4 — see issueBrowserTicket
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

	connReqUsername := os.Getenv("ACS_CONNECTION_REQUEST_USERNAME")
	connReqPassword := os.Getenv("ACS_CONNECTION_REQUEST_PASSWORD")
	if connReqUsername == "" {
		logger.Warn("ACS_CONNECTION_REQUEST_USERNAME/ACS_CONNECTION_REQUEST_PASSWORD not set — Connection Request GETs will be sent unauthenticated, and a 401 challenge cannot be answered (design §5.6/§12).")
	}
	worker := &connectionRequestWorker{
		logger:      logger,
		jobs:        h.jobs,
		devices:     h.devices,
		credentials: h.credentials,
		auditor:     observability.NewAuditor(db),
		username:    connReqUsername,
		password:    connReqPassword,
	}
	go worker.Run(ctx)

	scheduleW := &scheduleWorker{
		logger:    logger,
		schedules: h.schedules,
		jobs:      h.jobs,
		devices:   h.devices,
		groups:    h.groups,
		auditor:   observability.NewAuditor(db),
	}
	go scheduleW.Run(ctx)

	apiRate := envOrFloat("ACS_API_RATE_LIMIT_PER_SECOND", defaultAPIRateLimitPerSecond)
	apiBurst := envOrInt("ACS_API_RATE_LIMIT_BURST", defaultAPIRateLimitBurst)
	apiLimiter := ratelimit.New(apiRate, apiBurst, rateLimitIdleTTL)
	logger.Info("cmd/api rate limit configured", "per_second", apiRate, "burst", apiBurst)

	addr := envOr("ACS_API_ADDR", ":8080")
	corsOrigin := envOr("ACS_API_CORS_ORIGIN", "*")
	server := &http.Server{
		Addr:    addr,
		Handler: withCORS(corsOrigin, withJWTAuth(jwtSecret, internalServiceToken, withRateLimit(apiLimiter, metrics, withBodyLimit(mux)))),
		// Timeouts (audit P1.2). No global ReadTimeout/WriteTimeout on
		// purpose: firmware uploads and CPE upload receipts stream large
		// bodies, and the console WebSocket / web-GUI proxy are long-lived
		// — a global write deadline would cut those off. Bodies are
		// bounded instead by withBodyLimit (JSON routes), MaxBytesReader
		// (upload receipt), and ParseMultipartForm's cap (firmware).
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("REST API listening", "addr", addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		// Workers are already winding down (they share ctx). Give
		// in-flight requests a bounded window to finish, then exit.
		logger.Info("shutting down: draining in-flight requests")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("shutdown did not complete cleanly", "err", err)
		}
	}
}

// maxJSONBody caps request bodies on ordinary API routes (audit P1.2 /
// "request-body limits on JSON routes"). Nothing legitimate on these
// routes approaches this; the file-transfer routes below carry their
// own, larger, purpose-specific limits.
const (
	maxJSONBody   = 1 << 20  // 1 MiB
	maxImportBody = 32 << 20 // bulk device import (JSON/CSV/XML)
)

// withBodyLimit wraps every request body in a MaxBytesReader sized for
// its route class. Streaming/multipart routes are exempt because they
// bound themselves.
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(path, "/api/v1/uploads/") && strings.HasSuffix(path, "/receive"):
			// CPE upload receipt — bounded by ACS_UPLOAD_MAX_BYTES in the handler.
		case r.Method == http.MethodPost && path == "/api/v1/firmware/images":
			// multipart firmware publish — bounded by ParseMultipartForm.
		case r.Method == http.MethodPost && path == "/api/v1/devices/import":
			r.Body = http.MaxBytesReader(w, r.Body, maxImportBody)
		default:
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrFloat(key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(os.Getenv(key), 64)
	if err != nil {
		return fallback
	}
	return v
}

func envOrInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

// Rate limit defaults (build plan §7.4 sub-phase 7d — shipped after JWT
// auth, not before, per §7.3's own reasoning).
const (
	defaultAPIRateLimitPerSecond = 10
	defaultAPIRateLimitBurst     = 30
	rateLimitIdleTTL             = 10 * time.Minute
)

// withCORS lets the frontend dev server (a different origin — Vite on
// :5173, this API on :8080) call these endpoints from a browser. Runs
// outside withJWTAuth so a preflight OPTIONS never needs a token; origin
// restriction (ACS_API_CORS_ORIGIN) is the real boundary once that's set
// to something other than "*" — auth (withJWTAuth, added Phase 6) is what
// actually gates the requests themselves.
func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type handler struct {
	logger  *slog.Logger
	devices *devices.Repository
	jobs    *jobs.Repository
	params  *parameters.Repository
	vendors *adapters.Registry
	auditor *observability.Auditor

	firmware     *firmware.Repository
	firmwareFS   *firmware.Storage
	firmwareBase string // base URL the CPE fetches Download URLs from (design doc v3 §9.4's "HTTPS static file host", local-disk stand-in — see internal/firmware doc comment)

	operators *operators.Repository
	jwtSecret []byte // empty means operator auth is disabled — see main()'s ACS_JWT_SIGNING_SECRET warning

	// transferKey signs the expiring tokens on public firmware-download
	// and upload-receipt URLs (audit P0.3, internal/transfer). Derived
	// from the JWT secret; empty (dev mode only) disables enforcement.
	transferKey    []byte
	uploadMaxBytes int64

	// netPolicy restricts where the web-GUI proxy and console bridge
	// may dial (audit P0.4) — see ACS_DEVICE_NET_ALLOWED_CIDRS.
	netPolicy netguard.Policy

	metrics     *observability.Metrics
	groups      *devices.GroupRepository
	credentials *credentials.Repository
	schedules   *scheduler.Repository
	rollouts    *rollout.Repository
	policies    *policy.Repository

	uploads     *uploads.Repository
	uploadsFS   *uploads.Storage
	uploadsBase string // base URL the CPE PUTs Upload'd files back to, mirrors firmwareBase

	templates       *templates.Repository
	cli             *cliaccess.Repository
	permissions     *operators.PermissionRepository
	mailer          *mailer.Mailer
	frontendBaseURL string
	tenancy         *tenancy.Repository
	dashboards      *dashboard.Repository

	bssMappings     *bss.Repository
	bssWebhooks     *bss.WebhookRepository
	bssOAuthClients *bss.OAuthRepository
	bssAdapterURL   string // base URL of the live cmd/bssadapter process, for admin-panel troubleshooting calls
	bssToken        string // same shared token bssadapter itself expects, so admin-panel troubleshooting calls authenticate as a real BSS caller would
	bssHTTPClient   *http.Client

	vpnPeers        *vpn.Repository
	vpnConcentrator vpn.ConcentratorConfig
}

// deviceResponse is the v3 §8.1/§8.2 device shape, trimmed to the fields
// Phase 1/3 actually populate.
type deviceResponse struct {
	ID                          string   `json:"id"`
	OUISerial                   string   `json:"oui_serial"`
	Manufacturer                string   `json:"manufacturer"`
	OUI                         string   `json:"oui"`
	ProductClass                string   `json:"product_class"`
	SerialNumber                string   `json:"serial_number"`
	DataModelRoot               string   `json:"data_model_root"`
	OnlineStatus                string   `json:"online_status"`
	LastInformAt                *string  `json:"last_inform_at,omitempty"`
	LastInformEventCodes        []string `json:"last_inform_event_codes,omitempty"`
	ConnectionRequestURL        *string  `json:"connection_request_url,omitempty"`
	ConnectionRequestMode       string   `json:"connection_request_mode"`
	LastConnectionRequestAt     *string  `json:"last_connection_request_at,omitempty"`
	LastConnectionRequestStatus *string  `json:"last_connection_request_status,omitempty"`
	Tags                        []string `json:"tags,omitempty"`
	CWMPAuthMode                string   `json:"cwmp_auth_mode"`
	UDPConnectionRequestAddress *string  `json:"udp_connection_request_address,omitempty"`
	NATDetected                 *bool    `json:"nat_detected,omitempty"`
	CustomerID                  *string  `json:"customer_id,omitempty"`
	Location                    *string  `json:"location,omitempty"`
}

func toResponse(d devices.Device) deviceResponse {
	resp := deviceResponse{
		ID:                          d.ID,
		OUISerial:                   d.OUISerial,
		Manufacturer:                d.Manufacturer,
		OUI:                         d.OUI,
		ProductClass:                d.ProductClass,
		SerialNumber:                d.SerialNumber,
		DataModelRoot:               d.DataModelRoot,
		OnlineStatus:                d.OnlineStatus,
		LastInformEventCodes:        d.LastInformEventCodes,
		ConnectionRequestURL:        d.ConnectionRequestURL,
		ConnectionRequestMode:       d.ConnectionRequestMode,
		LastConnectionRequestStatus: d.LastConnectionRequestStatus,
		Tags:                        d.Tags,
		CWMPAuthMode:                d.CWMPAuthMode,
		UDPConnectionRequestAddress: d.UDPConnectionRequestAddress,
		NATDetected:                 d.NATDetected,
		CustomerID:                  d.CustomerID,
		Location:                    d.Location,
	}
	if d.LastInformAt != nil {
		s := d.LastInformAt.Format(time.RFC3339)
		resp.LastInformAt = &s
	}
	if d.LastConnectionRequestAt != nil {
		s := d.LastConnectionRequestAt.Format(time.RFC3339)
		resp.LastConnectionRequestAt = &s
	}
	return resp
}

// deviceScope resolves the calling operator's multi-tenancy scope
// (admin-platform backlog) into devices.ListParams' CustomerIDs/Scoped
// fields — unrestricted (Scoped: false) when auth is disabled, the caller
// is superadmin, or the operator has no scope rows assigned (the default,
// backward-compatible for every operator until a superadmin explicitly
// scopes one).
//
// Lookup failures return an error and the caller must fail the request
// (audit P0.2): the previous behavior of treating a failed scope
// resolution as "unrestricted" turned transient DB errors into a
// cross-tenant data exposure.
func (h *handler) deviceScope(r *http.Request) (customerIDs []string, scoped bool, err error) {
	if len(h.jwtSecret) == 0 {
		return nil, false, nil
	}
	claims, ok := operatorClaims(r.Context())
	if !ok || claims.Role == operators.RoleSuperAdmin {
		return nil, false, nil
	}
	op, err := h.operators.ByUsername(r.Context(), claims.Subject)
	if err != nil {
		h.logger.Error("failed to resolve operator for scoping", "err", err, "username", claims.Subject)
		return nil, false, fmt.Errorf("resolve operator for scoping: %w", err)
	}
	ids, isScoped, err := h.tenancy.AccessibleCustomerIDs(r.Context(), op.ID)
	if err != nil {
		h.logger.Error("failed to resolve operator scope", "err", err, "operator_id", op.ID)
		return nil, false, fmt.Errorf("resolve operator scope: %w", err)
	}
	return ids, isScoped, nil
}

// getScopedDevice loads a device and enforces the calling operator's
// tenancy scope in one place (audit P0.2) — every device-addressed
// handler goes through here instead of calling h.devices.Get directly.
// It writes the HTTP response itself on failure: 404 both when the
// device doesn't exist and when it is outside the caller's scope (a 403
// would confirm the device exists across a tenant boundary), 500 when
// the device or scope lookup errors — a failed scope resolution denies
// access, it no longer falls open.
func (h *handler) getScopedDevice(w http.ResponseWriter, r *http.Request, id string) (*devices.Device, bool) {
	d, err := h.devices.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if scoped && !deviceInScope(d.CustomerID, customerIDs) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	return d, true
}

func (h *handler) listDevices(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	result, err := h.devices.List(r.Context(), devices.ListParams{Page: page, PageSize: pageSize, CustomerIDs: customerIDs, Scoped: scoped})
	if err != nil {
		h.logger.Error("failed to list devices", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	items := make([]deviceResponse, 0, len(result.Items))
	for _, d := range result.Items {
		items = append(items, toResponse(d))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": result.Total,
	})
}

// listDevicesSummary backs a mass-review view: fleet counts grouped by
// vendor/status/reachability, computed in SQL so it stays cheap
// regardless of fleet size — the alternative (paging through every
// device to count client-side) is exactly the thing pagination above
// exists to avoid.
func (h *handler) listDevicesSummary(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	groups, err := h.devices.Summary(r.Context(), customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to summarize devices", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		// Summary returns a nil slice on a fleet with zero devices —
		// encodes as JSON null, which crashes Fleet Control's groups.map().
		groups = []devices.GroupCount{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// listMatchingDeviceIDs backs Fleet Control's "select all N matching this
// filter" — build plan §6.2's stated scope boundary (selection accumulated
// across pages, but nothing let an operator select everything matching a
// filter without paging through it by hand). Filters mirror what Fleet
// Control already computes client-side from the grouped summary strip and
// its search box.
func (h *handler) listMatchingDeviceIDs(w http.ResponseWriter, r *http.Request) {
	filter := devices.MatchingFilter{
		Manufacturer:          r.URL.Query().Get("manufacturer"),
		OnlineStatus:          r.URL.Query().Get("online_status"),
		ConnectionRequestMode: r.URL.Query().Get("connection_request_mode"),
		Search:                r.URL.Query().Get("search"),
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ids, err := h.devices.MatchingIDs(r.Context(), filter, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to list matching device ids", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids, "count": len(ids)})
}

// fleetHealth aggregates the signals design doc v3 §16.1 names for a
// health screen (inform rate, RPC fault rate, connection request success
// rate, device online/offline/unreachable counts) into one response —
// build plan's stated gap "Fleet Health screen — not built". Every number
// here is a live SQL aggregate, not a cached/estimated figure.
func (h *handler) fleetHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byStatus, err := h.devices.CountByOnlineStatus(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by online status", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byReachability, err := h.devices.CountByReachability(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by reachability", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	informRecency, err := h.devices.InformRecencyBuckets(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to bucket inform recency", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Job stats aren't scoped to the operator's devices — jobs don't carry
	// a customer_id of their own, and joining through devices for this one
	// read isn't worth it yet (build plan note, not a security gap: job
	// stats reveal fleet-wide operational health, not any specific
	// customer's device identities or data).
	jobStats, err := h.jobs.StatusCountsSince(ctx, time.Now().UTC().Add(-24*time.Hour), customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count job statuses", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	jobTotal := 0
	for _, n := range jobStats {
		jobTotal += n
	}
	successRate := 0.0
	if jobTotal > 0 {
		successRate = float64(jobStats["SUCCESS"]) / float64(jobTotal) * 100
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"devices_by_status":       byStatus,
		"devices_by_reachability": byReachability,
		"inform_recency":          informRecency,
		"jobs_last_24h":           jobStats,
		"jobs_last_24h_total":     jobTotal,
		"job_success_rate_pct":    successRate,
		"generated_at":            time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *handler) getDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toResponse(*d))
}

func deviceInScope(deviceCustomerID *string, accessibleCustomerIDs []string) bool {
	if deviceCustomerID == nil {
		return false // unassigned devices are invisible to a scoped operator, not implicitly shared
	}
	for _, id := range accessibleCustomerIDs {
		if id == *deviceCustomerID {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// cachedValueResponse mirrors design doc v3 §7.7's example cache entry
// shape.
type cachedValueResponse struct {
	Value     string `json:"value"`
	Type      string `json:"type,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Source    string `json:"source"`
}

// getParameters reads the device's parameter cache (design doc v3 §8.3:
// "Read cached parameters" — this is a cache read, not a live CPE query;
// PUT below is what queues a job that actually talks to the device).
// An optional ?paths=a,b,c filters to just those parameter names.
func (h *handler) getParameters(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	cached, err := h.params.Get(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to read parameter cache", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var wanted map[string]bool
	if raw := r.URL.Query().Get("paths"); raw != "" {
		wanted = map[string]bool{}
		for _, p := range strings.Split(raw, ",") {
			wanted[strings.TrimSpace(p)] = true
		}
	}

	out := make(map[string]cachedValueResponse, len(cached))
	for name, v := range cached {
		if wanted != nil && !wanted[name] {
			continue
		}
		out[name] = cachedValueResponse{Value: v.Value, Type: v.Type, UpdatedAt: v.UpdatedAt.Format(time.RFC3339), Source: v.Source}
	}

	writeJSON(w, http.StatusOK, map[string]any{"parameters": out})
}

// getParameterHistory backs the nice-to-have feature backlog's parameter
// value history: how a specific parameter's cached value has changed over
// time, not just its current reading.
func (h *handler) getParameterHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query parameter is required", http.StatusBadRequest)
		return
	}
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	entries, err := h.params.History(r.Context(), id, name)
	if err != nil {
		h.logger.Error("failed to read parameter history", "err", err, "id", id, "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		items = append(items, map[string]any{
			"value": e.Value, "type": e.Type, "source": e.Source, "recorded_at": e.RecordedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "items": items})
}

// parameterInput is the wire shape for a parameter write, shared by the
// single-device and bulk endpoints so the two don't drift.
type parameterInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

func buildSetParameterPayload(inputs []parameterInput) (jobs.SetParameterPayload, error) {
	if len(inputs) == 0 {
		return jobs.SetParameterPayload{}, errors.New("parameters must not be empty")
	}
	payload := jobs.SetParameterPayload{Parameters: make([]jobs.ParameterWrite, len(inputs))}
	for i, p := range inputs {
		if p.Name == "" {
			return jobs.SetParameterPayload{}, errors.New("each parameter requires a name")
		}
		payload.Parameters[i] = jobs.ParameterWrite{Name: p.Name, Value: p.Value, Type: p.Type}
	}
	return payload, nil
}

type putParametersRequest struct {
	Parameters []parameterInput `json:"parameters"`
}

// putParameters queues a SET_PARAMETER job and returns 202 Accepted
// (design doc v3 §8.3/§19.2 — REST write endpoints never talk to the CPE
// directly, they queue a job that the CWMP gateway dispatches on the
// device's next session).
func (h *handler) putParameters(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req putParametersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	payload, err := buildSetParameterPayload(req.Parameters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeSetParameter, payload, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue parameter write", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
	})
}

// maxBulkDevices caps one bulk-action request the same way maxPageSize
// caps one list response — a mass-control view is exactly the place an
// accidental "select all" against an 18,000-device fleet would otherwise
// try to fire 18,000 jobs from a single HTTP request.
const maxBulkDevices = 500

type bulkActionRequest struct {
	DeviceIDs      []string         `json:"device_ids,omitempty"`
	GroupID        string           `json:"group_id,omitempty"` // alternative to device_ids — targets a device_groups member set (build plan §4 Phase 7)
	Action         string           `json:"action"`             // SET_PARAMETER | CONNECTION_REQUEST | REFRESH_CELLULAR
	Parameters     []parameterInput `json:"parameters,omitempty"`
	TimeoutSeconds int              `json:"timeout_seconds,omitempty"`
}

type bulkActionResult struct {
	DeviceID   string `json:"device_id"`
	CommandKey string `json:"command_key,omitempty"`
	Error      string `json:"error,omitempty"`
}

// bulkAction is the "mass unit control" capability a fleet-scale review
// view needs and no single-device endpoint provides: one action fanned
// out to N devices, each getting its own independent job/command_key.
// A failure on one device (e.g. it no longer exists) doesn't block the
// others — the response reports per-device outcome so the caller can see
// exactly which ones didn't queue, rather than one all-or-nothing result
// hiding which devices actually got the action.
func (h *handler) bulkAction(w http.ResponseWriter, r *http.Request) {
	var req bulkActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if len(req.DeviceIDs) == 0 && req.GroupID != "" {
		memberIDs, err := h.groups.MemberDeviceIDs(r.Context(), req.GroupID)
		if err != nil {
			h.logger.Error("failed to resolve device group", "err", err, "group_id", req.GroupID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.DeviceIDs = memberIDs
	}
	if len(req.DeviceIDs) == 0 {
		http.Error(w, "device_ids (or a non-empty group_id) must be provided", http.StatusBadRequest)
		return
	}
	if len(req.DeviceIDs) > maxBulkDevices {
		http.Error(w, fmt.Sprintf("device_ids exceeds the %d-device limit per request", maxBulkDevices), http.StatusBadRequest)
		return
	}

	var setParamPayload jobs.SetParameterPayload
	if req.Action == jobs.TypeSetParameter {
		payload, err := buildSetParameterPayload(req.Parameters)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		setParamPayload = payload
	} else if req.Action != jobs.TypeConnectionRequest && req.Action != "REFRESH_CELLULAR" {
		http.Error(w, fmt.Sprintf("unsupported bulk action %q", req.Action), http.StatusBadRequest)
		return
	}

	// Per-device tenancy enforcement (audit P0.2): a scoped operator's
	// bulk request must not fan out to devices outside their scope —
	// out-of-scope IDs report the same "not found" a single-device
	// endpoint would return, without blocking the in-scope remainder.
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	operator := operatorFromRequest(r)
	results := make([]bulkActionResult, 0, len(req.DeviceIDs))

	for _, deviceID := range req.DeviceIDs {
		result := bulkActionResult{DeviceID: deviceID}

		if scoped {
			d, err := h.devices.Get(r.Context(), deviceID)
			if err != nil || !deviceInScope(d.CustomerID, customerIDs) {
				result.Error = "not found"
				results = append(results, result)
				continue
			}
		}

		switch req.Action {
		case jobs.TypeSetParameter:
			job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeSetParameter, setParamPayload, operator)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.CommandKey = job.CommandKey
			}

		case jobs.TypeConnectionRequest:
			job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeConnectionRequest,
				jobs.ConnectionRequestPayload{TimeoutSeconds: req.TimeoutSeconds}, operator)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.CommandKey = job.CommandKey
			}

		case "REFRESH_CELLULAR":
			device, err := h.devices.Get(r.Context(), deviceID)
			if err != nil {
				result.Error = "device not found"
				break
			}
			_, paths := h.vendors.MatchCellularDiagnostics(device.Manufacturer)
			job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, operator)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.CommandKey = job.CommandKey
			}
		}

		results = append(results, result)
	}

	succeeded := 0
	for _, res := range results {
		if res.Error == "" {
			succeeded++
		}
	}

	if err := h.auditor.Record(r.Context(), operator, "", "BulkAction", map[string]any{
		"action": req.Action, "device_count": len(req.DeviceIDs), "succeeded": succeeded,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("bulk action dispatched", "action", req.Action, "devices", len(req.DeviceIDs), "succeeded", succeeded)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"action":    req.Action,
		"requested": len(req.DeviceIDs),
		"succeeded": succeeded,
		"results":   results,
	})
}

type createConnectionRequestRequest struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

type reachabilityInfo struct {
	ConnectionRequestMode string `json:"connection_request_mode"`
	Recommendation        string `json:"recommendation"`
}

// createDiagnosticsPingRequest mirrors design doc v3 §10.1's IPPing input
// parameters. Only Host is required; the rest default to sane values a
// GenieACS-style ping test would use.
type createDiagnosticsPingRequest struct {
	Host                string `json:"host"`
	NumberOfRepetitions int    `json:"number_of_repetitions,omitempty"`
	Timeout             int    `json:"timeout,omitempty"`
	DataBlockSize       int    `json:"data_block_size,omitempty"`
	DSCP                int    `json:"dscp,omitempty"`
}

const (
	diagPingDefaultRepetitions = 4
	diagPingDefaultTimeoutMS   = 5000
	diagPingDefaultBlockSize   = 64
	// diagPingMaxAttempts caps how many times cmd/acs will requeue a
	// DIAGNOSTICS_PING job for another poll before giving up as a
	// TIMEOUT — a device whose DiagnosticsState never leaves "Requested"
	// would otherwise poll forever (build plan §4 Phase 5).
	diagPingMaxAttempts = 15
)

// createConnectionRequest queues a CONNECTION_REQUEST job (design doc v3
// §8.4). Unlike PUT parameters, this never talks to the CPE synchronously
// from the request handler — the background worker in connreq_worker.go
// picks it up. reachability in the response reflects the device's
// *current* known classification from previous attempts, not this
// attempt's outcome, which is still pending when this responds.
func (h *handler) createConnectionRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req createConnectionRequestRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeConnectionRequest,
		jobs.ConnectionRequestPayload{TimeoutSeconds: req.TimeoutSeconds}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue connection request", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	recommendation := "Device may rely on periodic Inform until reachability is verified."
	if device.ConnectionRequestMode == devices.ModeDirectIPv4 || device.ConnectionRequestMode == devices.ModeDirectIPv6 {
		recommendation = "Device has previously confirmed direct reachability."
	} else if device.ConnectionRequestMode == devices.ModePeriodicFallback {
		recommendation = "Device has previously failed to respond to Connection Request within the wait window (likely CGNAT) — periodic Inform is the reliable path."
	}
	if device.ConnectionRequestURL == nil || *device.ConnectionRequestURL == "" {
		recommendation = "No ConnectionRequestURL known for this device yet — it must send at least one Inform first."
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
		"reachability": reachabilityInfo{
			ConnectionRequestMode: device.ConnectionRequestMode,
			Recommendation:        recommendation,
		},
	})
}

// refreshCellularDiagnostics queues a GET_PARAMETER job for the device's
// vendor-specific RF/signal diagnostic parameters (RSRP, RSRQ, SINR,
// CellID, ...), matched by the device's reported manufacturer against the
// vendor catalogs in internal/devices/adapters. This is the same
// branch-by-manufacturer pattern a GenieACS provision script uses to poll
// 5G signal metrics, built on the GET_PARAMETER job type Phase 2 already
// has — falls back to the generic TR-181 Cellular path set for an
// unrecognized vendor rather than failing.
func (h *handler) refreshCellularDiagnostics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	vendor, paths := h.vendors.MatchCellularDiagnostics(device.Manufacturer)

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue cellular diagnostics refresh", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key":    job.CommandKey,
		"status":         job.Status,
		"matched_vendor": vendor,
		"parameters":     paths,
	})
}

// refreshWifiClients queues a GET_PARAMETER job over the whole WiFi
// AccessPoint subtree — every SSID's AssociatedDevice table included —
// mirroring refreshCellularDiagnostics' shape.
func (h *handler) refreshWifiClients(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	paths := []string{adapters.WiFiAssociatedDevicesPrefix(device.DataModelRoot)}
	job, err := h.jobs.Create(r.Context(), id, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue wifi clients refresh", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
		"parameters":  paths,
	})
}

// createDiagnosticsPing queues a DIAGNOSTICS_PING job (design doc v3
// §10.1). Unlike the other job types, this one polls itself to
// completion — cmd/acs's dispatch loop requeues the same job for another
// GetParameterValues poll until DiagnosticsState leaves "Requested"
// instead of finalizing after one round-trip. The REST response here is
// just "queued"; poll GET /jobs/{command_key} for the outcome, same as
// CONNECTION_REQUEST and FIRMWARE_DOWNLOAD.
func (h *handler) createDiagnosticsPing(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req createDiagnosticsPingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	if req.NumberOfRepetitions == 0 {
		req.NumberOfRepetitions = diagPingDefaultRepetitions
	}
	if req.Timeout == 0 {
		req.Timeout = diagPingDefaultTimeoutMS
	}
	if req.DataBlockSize == 0 {
		req.DataBlockSize = diagPingDefaultBlockSize
	}

	job, err := h.jobs.CreateWithMaxAttempts(r.Context(), id, jobs.TypeDiagnosticsPing,
		jobs.DiagnosticsPingPayload{
			Host:                req.Host,
			NumberOfRepetitions: req.NumberOfRepetitions,
			Timeout:             req.Timeout,
			DataBlockSize:       req.DataBlockSize,
			DSCP:                req.DSCP,
			Prefix:              adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticPing),
		}, operatorFromRequest(r), diagPingMaxAttempts)
	if err != nil {
		h.logger.Error("failed to queue diagnostics ping", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
	})
}

// createDiagnosticsTracerouteRequest mirrors design doc v3 §10.1's
// TraceRoute input parameters — build plan §4 Phase 5's explicitly
// deferred item ("identical pattern [to Ping], not committed to this
// session"), built here.
type createDiagnosticsTracerouteRequest struct {
	Host          string `json:"host"`
	NumberOfTries int    `json:"number_of_tries,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
	DataBlockSize int    `json:"data_block_size,omitempty"`
	DSCP          int    `json:"dscp,omitempty"`
	MaxHopCount   int    `json:"max_hop_count,omitempty"`
}

const (
	diagTracerouteDefaultTries     = 3
	diagTracerouteDefaultTimeoutMS = 5000
	diagTracerouteDefaultBlockSize = 38
	diagTracerouteDefaultMaxHops   = 30
	// diagTracerouteMaxAttempts mirrors diagPingMaxAttempts — same poll-
	// loop shape, same reason for a cap (build plan §4 Phase 5).
	diagTracerouteMaxAttempts = 15
)

func (h *handler) createDiagnosticsTraceroute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req createDiagnosticsTracerouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	if req.NumberOfTries == 0 {
		req.NumberOfTries = diagTracerouteDefaultTries
	}
	if req.Timeout == 0 {
		req.Timeout = diagTracerouteDefaultTimeoutMS
	}
	if req.DataBlockSize == 0 {
		req.DataBlockSize = diagTracerouteDefaultBlockSize
	}
	if req.MaxHopCount == 0 {
		req.MaxHopCount = diagTracerouteDefaultMaxHops
	}

	job, err := h.jobs.CreateWithMaxAttempts(r.Context(), id, jobs.TypeDiagnosticsTraceroute,
		jobs.DiagnosticsTraceroutePayload{
			Host:          req.Host,
			NumberOfTries: req.NumberOfTries,
			Timeout:       req.Timeout,
			DataBlockSize: req.DataBlockSize,
			DSCP:          req.DSCP,
			MaxHopCount:   req.MaxHopCount,
			Prefix:        adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticTraceroute),
		}, operatorFromRequest(r), diagTracerouteMaxAttempts)
	if err != nil {
		h.logger.Error("failed to queue diagnostics traceroute", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
	})
}

// jobResponse mirrors design doc v3 §8.5's job status shape.
type jobResponse struct {
	CommandKey   string          `json:"command_key"`
	DeviceID     string          `json:"device_id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	CompletedAt  *string         `json:"completed_at,omitempty"`
	FaultCode    *string         `json:"fault_code,omitempty"`
	FaultString  *string         `json:"fault_string,omitempty"`
	ResultDetail json.RawMessage `json:"result_detail,omitempty"`
}

func toJobResponse(job *jobs.Job) jobResponse {
	resp := jobResponse{
		CommandKey:   job.CommandKey,
		DeviceID:     job.DeviceID,
		Type:         job.Type,
		Status:       job.Status,
		CreatedAt:    job.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    job.UpdatedAt.Format(time.RFC3339),
		FaultCode:    job.FaultCode,
		FaultString:  job.FaultString,
		ResultDetail: job.ResultDetail,
	}
	if job.CompletedAt != nil {
		s := job.CompletedAt.Format(time.RFC3339)
		resp.CompletedAt = &s
	}
	return resp
}

// listJobs backs the Jobs screen — every job across the fleet, most
// recent first, capped at jobs.listLimit since there's no pagination yet.
func (h *handler) listJobs(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	list, err := h.jobs.List(r.Context(), "", customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to list jobs", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]jobResponse, 0, len(list))
	for i := range list {
		items = append(items, toJobResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// listDeviceJobs backs Device Detail's recent-activity panel — one
// device's jobs, most recent first.
func (h *handler) listDeviceJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	list, err := h.jobs.List(r.Context(), id, nil, false)
	if err != nil {
		h.logger.Error("failed to list device jobs", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]jobResponse, 0, len(list))
	for i := range list {
		items = append(items, toJobResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (h *handler) getJob(w http.ResponseWriter, r *http.Request) {
	commandKey := r.PathValue("command_key")
	job, err := h.jobs.ByCommandKey(r.Context(), commandKey)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to get job", "err", err, "command_key", commandKey)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A job belongs to a device; the caller must be in that device's
	// tenancy scope (audit P0.2) — same 404-not-403 shape as devices.
	if _, ok := h.getScopedDevice(w, r, job.DeviceID); !ok {
		return
	}

	writeJSON(w, http.StatusOK, toJobResponse(job))
}

// operatorFromRequest reports who's making this request, for the audit
// trail and jobs.created_by. When JWT auth is enabled (ACS_JWT_SIGNING_SECRET
// set), this is the authenticated operator's username, put in context by
// withJWTAuth. When auth is disabled (lab mode — see that middleware),
// there's no real identity to report, so it falls back to the same
// generic "operator" Phase 2-5 used.
func operatorFromRequest(r *http.Request) string {
	if claims, ok := operatorClaims(r.Context()); ok {
		return claims.Subject
	}
	return "operator"
}
