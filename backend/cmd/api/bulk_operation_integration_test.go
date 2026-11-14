package main

import (
	"encoding/json"
	"strings"
	"testing"

	"acs/internal/jobs"
	"acs/internal/operators"
	"acs/internal/tenancy"
)

type bulkOpView struct {
	Operation struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Label     string `json:"label"`
		State     string `json:"state"`
		Total     int    `json:"total"`
		Queued    int    `json:"queued"`
		Succeeded int    `json:"succeeded"`
		Failed    int    `json:"failed"`
		Cancelled int    `json:"cancelled"`
		NotQueued int    `json:"not_queued"`
	} `json:"operation"`
	Items []struct {
		DeviceID   string  `json:"device_id"`
		CommandKey *string `json:"command_key"`
		JobStatus  *string `json:"job_status"`
		Error      *string `json:"error"`
		Retries    int     `json:"retries"`
	} `json:"items"`
	Cancelled int            `json:"cancelled"`
	Retried   int            `json:"retried"`
	Skipped   map[string]int `json:"skipped"`
}

func decodeBulkOp(t *testing.T, r resp, want int) bulkOpView {
	t.Helper()
	if r.code != want {
		t.Fatalf("bulk operation → %d %s, want %d", r.code, r.body, want)
	}
	var v bulkOpView
	if err := json.Unmarshal([]byte(r.body), &v); err != nil {
		t.Fatalf("decode bulk operation: %v (%s)", err, r.body)
	}
	return v
}

// TestIntegration_BulkOperationLifecycle: a bulk action is recorded as an
// operation whose progress follows its jobs; the queued remainder can be
// cancelled and the failures retried; a scoped operator sees only their
// own devices in it.
func TestIntegration_BulkOperationLifecycle(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Customer A"), e.customer("Customer B")
	a1, a2, a3 := e.device("A001", &custA), e.device("A002", &custA), e.device("A003", &custA)
	b1 := e.device("B001", &custB)
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("bob", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custB})
	e.operator("viewer", operators.RoleReadOnly, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.grant(operators.RoleNOC, operators.PermBulkActions, operators.PermConnectionReq)

	r := e.call("root", "POST", "/api/v1/devices/bulk-actions", map[string]any{
		"action": "CONNECTION_REQUEST", "device_ids": []string{a1, a2, a3, b1, "no-such-device"},
	})
	if r.code != 202 {
		t.Fatalf("bulk → %d %s", r.code, r.body)
	}
	var started struct {
		ID      string `json:"bulk_operation_id"`
		Results []struct {
			DeviceID   string `json:"device_id"`
			CommandKey string `json:"command_key"`
		} `json:"results"`
	}
	_ = json.Unmarshal([]byte(r.body), &started)
	if started.ID == "" {
		t.Fatalf("no bulk_operation_id in %s", r.body)
	}
	path := "/api/v1/bulk-operations/" + started.ID
	keyOf := map[string]string{}
	for _, res := range started.Results {
		keyOf[res.DeviceID] = res.CommandKey
	}
	jobID := func(device string) string {
		j, err := e.h.jobs.ByCommandKey(e.ctx, keyOf[device])
		if err != nil {
			t.Fatalf("job for %s: %v", device, err)
		}
		return j.ID
	}

	// a1 succeeds, a2 fails, a3 and b1 stay queued.
	if err := e.h.jobs.MarkSuccess(e.ctx, jobID(a1)); err != nil {
		t.Fatal(err)
	}
	if err := e.h.jobs.MarkFailed(e.ctx, jobID(a2), "9002", "internal error"); err != nil {
		t.Fatal(err)
	}

	t.Run("progress follows the jobs", func(t *testing.T) {
		v := decodeBulkOp(t, e.call("root", "GET", path, nil), 200)
		op := v.Operation
		// "no-such-device" queues a job row that no device backs? It must not:
		// every requested ID is an item, queued or not.
		if op.Total != 5 || op.Succeeded != 1 || op.Failed != 1 || op.Queued+op.NotQueued != 3 {
			t.Fatalf("counts: %+v", op)
		}
		if op.State != "RUNNING" || op.Kind != "BULK_ACTION" || op.Label != "Connection request" {
			t.Errorf("operation: %+v", op)
		}
		if len(v.Items) != 5 || v.Items[0].DeviceID != a1 || v.Items[4].DeviceID != "no-such-device" {
			t.Errorf("items out of request order: %+v", v.Items)
		}
	})

	t.Run("scoped operator sees only own devices", func(t *testing.T) {
		v := decodeBulkOp(t, e.call("alice", "GET", path, nil), 200)
		if v.Operation.Total != 3 || len(v.Items) != 3 {
			t.Fatalf("alice sees %d items / total %d, want 3", len(v.Items), v.Operation.Total)
		}
		for _, it := range v.Items {
			if it.DeviceID == b1 {
				t.Errorf("alice sees customer B's device")
			}
		}
		v = decodeBulkOp(t, e.call("bob", "GET", path, nil), 200)
		if v.Operation.Total != 1 || v.Items[0].DeviceID != b1 {
			t.Fatalf("bob: %+v", v)
		}
		var list struct {
			Items []struct{ ID string } `json:"items"`
			Total int                   `json:"total"`
		}
		_ = json.Unmarshal([]byte(e.call("alice", "GET", "/api/v1/bulk-operations", nil).body), &list)
		if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != started.ID {
			t.Errorf("alice's list: %+v", list)
		}
	})

	t.Run("changing one needs the bulk permission", func(t *testing.T) {
		if r := e.call("viewer", "POST", path+"/cancel", nil); r.code != 403 {
			t.Errorf("readonly cancel → %d, want 403", r.code)
		}
		if r := e.call("viewer", "GET", path, nil); r.code != 200 {
			t.Errorf("readonly read → %d, want 200", r.code)
		}
	})

	t.Run("scoped cancel touches only own devices", func(t *testing.T) {
		v := decodeBulkOp(t, e.call("alice", "POST", path+"/cancel", nil), 200)
		if v.Cancelled != 1 || v.Operation.Cancelled != 1 {
			t.Fatalf("alice cancelled %d, want 1 (a3): %+v", v.Cancelled, v.Operation)
		}
		if j, _ := e.h.jobs.ByID(e.ctx, jobID(b1)); j.Status != jobs.StatusQueued {
			t.Errorf("customer B's job is %s after alice's cancel, want QUEUED", j.Status)
		}
		if j, _ := e.h.jobs.ByID(e.ctx, jobID(a1)); j.Status != jobs.StatusSuccess {
			t.Errorf("finished job changed to %s", j.Status)
		}
	})

	t.Run("retry failed queues new jobs and repoints the items", func(t *testing.T) {
		oldA2 := keyOf[a2]
		v := decodeBulkOp(t, e.call("alice", "POST", path+"/retry-failed", nil), 202)
		if v.Retried != 1 { // a2 failed; a3 was cancelled on purpose and stays so
			t.Fatalf("retried %d, want 1: skipped=%v", v.Retried, v.Skipped)
		}
		if v.Operation.Cancelled != 1 {
			t.Errorf("the cancelled job was retried: %+v", v.Operation)
		}
		for _, it := range v.Items {
			if it.DeviceID != a2 {
				continue
			}
			if it.CommandKey == nil || *it.CommandKey == oldA2 || it.JobStatus == nil || *it.JobStatus != "QUEUED" || it.Retries != 1 {
				t.Errorf("a2 after retry: %+v", it)
			}
		}
		if old, _ := e.h.jobs.ByCommandKey(e.ctx, oldA2); old.Status != jobs.StatusFailed {
			t.Errorf("original job is %s, want it left FAILED", old.Status)
		}
		// Nothing left to retry: a second call is a no-op.
		if v := decodeBulkOp(t, e.call("alice", "POST", path+"/retry-failed", nil), 202); v.Retried != 0 {
			t.Errorf("second retry queued %d more", v.Retried)
		}
	})

	t.Run("unknown operation", func(t *testing.T) {
		for _, p := range []string{"/api/v1/bulk-operations/00000000-0000-0000-0000-000000000000", "/api/v1/bulk-operations/not-a-uuid"} {
			if r := e.call("root", "GET", p, nil); r.code != 404 {
				t.Errorf("GET %s → %d, want 404", p, r.code)
			}
		}
	})
}

// TestIntegration_JobCancelAndRetry covers the single-job endpoints: what
// can be cancelled, what can be retried, and what each refusal says.
func TestIntegration_JobCancelAndRetry(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Customer A"), e.customer("Customer B")
	dev, foreign := e.device("A001", &custA), e.device("B001", &custB)
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.grant(operators.RoleNOC, operators.PermConnectionReq)

	create := func(device, jobType string, payload any, by string) *jobs.Job {
		t.Helper()
		j, err := e.h.jobs.Create(e.ctx, device, jobType, payload, by)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	status := func(r resp) string {
		var j struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal([]byte(r.body), &j)
		return j.Status
	}

	t.Run("cancel a queued job", func(t *testing.T) {
		j := create(dev, jobs.TypeReboot, struct{}{}, "root")
		r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/cancel", nil)
		if r.code != 200 || status(r) != "CANCELLED" {
			t.Fatalf("cancel → %d %s", r.code, r.body)
		}
		// The gateway must not pick it up.
		if leased, err := e.h.jobs.Lease(e.ctx, dev); err == nil && leased != nil && leased.ID == j.ID {
			t.Fatal("a cancelled job was leased")
		}
		if r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/cancel", nil); r.code != 409 {
			t.Errorf("second cancel → %d, want 409", r.code)
		}
	})

	t.Run("a job already sent cannot be cancelled", func(t *testing.T) {
		j := create(dev, jobs.TypeReboot, struct{}{}, "root")
		if leased, err := e.h.jobs.Lease(e.ctx, dev); err != nil || leased == nil || leased.ID != j.ID {
			t.Fatalf("lease: %v %v", leased, err)
		}
		r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/cancel", nil)
		if r.code != 409 || !strings.Contains(r.body, "queued") {
			t.Errorf("cancel of a sent job → %d %s, want 409", r.code, r.body)
		}
		_ = e.h.jobs.MarkSuccess(e.ctx, j.ID)
	})

	t.Run("system and workflow jobs are refused", func(t *testing.T) {
		for _, by := range []string{"system:wifi-insights", "service:bssadapter", "onboarding:123", "bss-client:naas"} {
			j := create(dev, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: []string{"Device."}}, by)
			if r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/cancel", nil); r.code != 409 || !strings.Contains(r.body, "managed by") {
				t.Errorf("cancel of %q job → %d %s, want 409", by, r.code, r.body)
			}
			_ = e.h.jobs.MarkFailed(e.ctx, j.ID, "9002", "x")
			if r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/retry", nil); r.code != 409 {
				t.Errorf("retry of %q job → %d, want 409", by, r.code)
			}
		}
		// An operator whose name merely starts like a system prefix is not one.
		j := create(dev, jobs.TypeReboot, struct{}{}, "systemsadmin")
		if r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/cancel", nil); r.code != 200 {
			t.Errorf("cancel of operator 'systemsadmin' job → %d %s, want 200", r.code, r.body)
		}
	})

	t.Run("retry", func(t *testing.T) {
		j := create(dev, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "root")
		if r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/retry", nil); r.code != 409 {
			t.Errorf("retry of a queued job → %d, want 409", r.code)
		}
		_ = e.h.jobs.MarkFailed(e.ctx, j.ID, "9002", "internal error")
		r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/retry", nil)
		if r.code != 202 || status(r) != "QUEUED" {
			t.Fatalf("retry → %d %s", r.code, r.body)
		}
		var nj struct {
			CommandKey string `json:"command_key"`
		}
		_ = json.Unmarshal([]byte(r.body), &nj)
		fresh, err := e.h.jobs.ByCommandKey(e.ctx, nj.CommandKey)
		if err != nil || fresh.ID == j.ID || string(fresh.Payload) != string(j.Payload) || fresh.DeviceID != dev {
			t.Errorf("retried job: %+v err=%v", fresh, err)
		}
	})

	t.Run("a job whose secret was erased is not retried", func(t *testing.T) {
		j := create(dev, jobs.TypeSetParameter, jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{
			{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: "correct horse battery", Type: "xsd:string"},
		}}, "root")
		_ = e.h.jobs.MarkFailed(e.ctx, j.ID, "9002", "internal error")
		r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/retry", nil)
		if r.code != 409 || !strings.Contains(r.body, "erased") {
			t.Errorf("retry → %d %s, want 409 naming the erased secret", r.code, r.body)
		}
	})

	t.Run("a cancelled job's secret is erased too", func(t *testing.T) {
		j := create(dev, jobs.TypeSetParameter, jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{
			{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: "correct horse battery", Type: "xsd:string"},
		}}, "root")
		if r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/cancel", nil); r.code != 200 {
			t.Fatalf("cancel → %d %s", r.code, r.body)
		}
		after, _ := e.h.jobs.ByID(e.ctx, j.ID)
		if strings.Contains(string(after.Payload), "correct horse") {
			t.Errorf("cancelled job still holds the passphrase: %s", after.Payload)
		}
	})

	t.Run("types that are not safe to repeat", func(t *testing.T) {
		j := create(dev, jobs.TypeFactoryReset, struct{}{}, "root")
		_ = e.h.jobs.MarkFailed(e.ctx, j.ID, "9002", "x")
		if r := e.call("root", "POST", "/api/v1/jobs/"+j.CommandKey+"/retry", nil); r.code != 409 {
			t.Errorf("retry of FACTORY_RESET → %d, want 409", r.code)
		}
	})

	t.Run("scope and permission", func(t *testing.T) {
		theirs := create(foreign, jobs.TypeConnectionRequest, jobs.ConnectionRequestPayload{}, "root")
		if r := e.call("alice", "POST", "/api/v1/jobs/"+theirs.CommandKey+"/cancel", nil); r.code != 404 {
			t.Errorf("alice cancels another customer's job → %d, want 404", r.code)
		}
		// alice holds connection_request but not devices.write.
		if err := e.h.permissions.Set(e.ctx, operators.RoleNOC, operators.PermDevicesWrite, false); err != nil {
			t.Fatal(err)
		}
		cr := create(dev, jobs.TypeConnectionRequest, jobs.ConnectionRequestPayload{}, "alice")
		if r := e.call("alice", "POST", "/api/v1/jobs/"+cr.CommandKey+"/cancel", nil); r.code != 200 {
			t.Errorf("alice cancels own connection request → %d %s, want 200", r.code, r.body)
		}
		reboot := create(dev, jobs.TypeReboot, struct{}{}, "root")
		if r := e.call("alice", "POST", "/api/v1/jobs/"+reboot.CommandKey+"/cancel", nil); r.code != 403 {
			t.Errorf("alice cancels a reboot without devices.write → %d, want 403", r.code)
		}
		if r := e.call("root", "POST", "/api/v1/jobs/nope_123/cancel", nil); r.code != 404 {
			t.Errorf("unknown job → %d, want 404", r.code)
		}
	})
}

// The jobs list names each device, so the console does not have to show
// an internal ID.
func TestIntegration_JobsListNamesDevices(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Customer A"), e.customer("Customer B")
	mine, theirs := e.device("A001", &custA), e.device("B001", &custB)
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	if _, err := e.db.ExecContext(e.ctx, `UPDATE devices SET label = 'Branch router' WHERE id = $1`, mine); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{mine, theirs} {
		if _, err := e.h.jobs.Create(e.ctx, d, jobs.TypeReboot, struct{}{}, "root"); err != nil {
			t.Fatal(err)
		}
	}
	var list struct {
		Items []struct {
			DeviceID    string `json:"device_id"`
			OUISerial   string `json:"oui_serial"`
			DeviceLabel string `json:"device_label"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(e.call("alice", "GET", "/api/v1/jobs", nil).body), &list); err != nil {
		t.Fatal(err)
	}
	// Scoping is unchanged: alice sees her customer's job only, now named.
	if len(list.Items) != 1 || list.Items[0].DeviceID != mine || list.Items[0].OUISerial != "ABCDEF-A001" || list.Items[0].DeviceLabel != "Branch router" {
		t.Fatalf("alice's jobs: %+v", list.Items)
	}
}
