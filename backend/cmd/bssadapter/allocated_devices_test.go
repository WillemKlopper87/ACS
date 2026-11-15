package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestStrictDeviceOwnership: with ACS_BSS_REQUIRE_ALLOCATED_DEVICES on, an
// ISP's integration can take only a device the operator has allocated to
// that ISP. Naming a serial first is not ownership: serials are printed on
// the box, and the integration writes its own sales.
func TestStrictDeviceOwnership(t *testing.T) {
	e := newBindingEnv(t)
	e.h.mappings.RequireAllocatedDevices(true)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)

	// The operator allocates DEV-A to customer A. DEV-B stays unallocated.
	if _, err := e.db.ExecContext(e.ctx, `UPDATE devices SET customer_id = $1::uuid WHERE oui_serial = '001122-DEV-A'`, e.custA); err != nil {
		t.Fatal(err)
	}

	t.Run("an allocated device maps as before", func(t *testing.T) {
		rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA, map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"})
		if rec.Code != http.StatusOK {
			t.Fatalf("A maps its allocated device = %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("an unallocated device cannot be claimed", func(t *testing.T) {
		rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA, map[string]any{"account_id": "A.2", "oui_serial": "001122-DEV-B"})
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ErrDeviceNotMapped") {
			t.Fatalf("A claims an unallocated device = %d %s, want 404", rec.Code, rec.Body)
		}
		if got := e.deviceCustomer(t, "DEV-B"); got != "" {
			t.Fatalf("the unallocated device was given to %q", got)
		}
	})

	t.Run("the refusal is the same as for another customer's device", func(t *testing.T) {
		unallocated := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokB, map[string]any{"account_id": "B.1", "oui_serial": "001122-DEV-B"})
		anothers := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokB, map[string]any{"account_id": "B.1", "oui_serial": "001122-DEV-A"})
		unknown := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokB, map[string]any{"account_id": "B.1", "oui_serial": "001122-NO-SUCH"})
		for name, rec := range map[string]int{"unallocated": unallocated.Code, "another customer's": anothers.Code, "unknown": unknown.Code} {
			if rec != http.StatusNotFound {
				t.Errorf("%s device → %d, want 404", name, rec)
			}
		}
		// Nothing in the answers tells the three apart beyond the serial echoed back.
		strip := func(body, serial string) string { return strings.ReplaceAll(body, serial, "X") }
		if a, b := strip(unallocated.Body.String(), "001122-DEV-B"), strip(anothers.Body.String(), "001122-DEV-A"); a != b {
			t.Errorf("unallocated and another customer's device answer differently:\n%s\n%s", a, b)
		}
	})

	t.Run("a serial not allocated to the customer cannot be pre-provisioned", func(t *testing.T) {
		// Never seen by the ACS at all.
		rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
			map[string]any{"account_id": "A.3", "oui_serial": "001122-NEW1", "pre_provision": true})
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not allocated to your customer") {
			t.Fatalf("pre-provision of an unknown serial = %d %s, want 409", rec.Code, rec.Body)
		}
		if _, err := e.h.onboarding.OpenBySerial(e.ctx, "NEW1"); err == nil {
			t.Fatal("a sale was recorded for a serial that is not the customer's")
		}
	})

	t.Run("a serial the operator registered for the customer can be", func(t *testing.T) {
		// What the device import does: a row with the serial and the customer,
		// before the device has ever connected.
		if _, err := e.db.ExecContext(e.ctx, `INSERT INTO devices (id, oui_serial, oui, serial_number, customer_id)
			VALUES (gen_random_uuid(), '001122+CPE+NEW2', '001122', 'NEW2', $1::uuid)`, e.custA); err != nil {
			t.Fatal(err)
		}
		// It has not informed, so the mapping pre-provisions... unless the
		// row already resolves, in which case it simply maps. Either is right.
		rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
			map[string]any{"account_id": "A.4", "oui_serial": "001122+CPE+NEW2", "pre_provision": true})
		if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
			t.Fatalf("mapping a registered serial = %d %s", rec.Code, rec.Body)
		}
		// Customer B cannot pre-provision the same serial, and is told no more
		// than "not allocated to your customer".
		rec = e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokB,
			map[string]any{"account_id": "B.4", "oui_serial": "001122+CPE+NEW2", "pre_provision": true})
		if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted || strings.Contains(rec.Body.String(), "another customer") {
			t.Fatalf("B on A's registered serial = %d %s", rec.Code, rec.Body)
		}
	})
}

// With the setting off (the default), an unassigned device still joins the
// customer that maps it: nothing changes for existing deployments.
func TestStrictDeviceOwnershipIsOptIn(t *testing.T) {
	e := newBindingEnv(t)
	if e.h.mappings.AllocatedDevicesRequired() {
		t.Fatal("strict device ownership is on by default")
	}
	rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", bindingToken(t, e.custA, false), map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"})
	if rec.Code != http.StatusOK || e.deviceCustomer(t, "DEV-A") != e.custA {
		t.Fatalf("default behaviour changed: %d %s", rec.Code, rec.Body)
	}
}
