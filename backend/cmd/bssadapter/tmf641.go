package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"acs/internal/bss"
	"acs/internal/onboarding"
	"acs/internal/tmf"
)

type tmf641ItemRequest struct {
	ID             string            `json:"id"`
	Action         string            `json:"action"`
	Role           string            `json:"role"`
	OUISerial      string            `json:"ouiSerial"`
	ServicePlan    string            `json:"servicePlan"`
	UnassignReason string            `json:"unassignReason"`
	Parameters     map[string]string `json:"parameters"`
	// Pre-provisioning: the SIM sold with the CPE and where it goes, used
	// when the CPE has not connected yet (preprovision.go).
	ICCID string       `json:"iccid"`
	IMSI  string       `json:"imsi"`
	Place *tmf641Place `json:"place"`
}
type tmf641OrderRequest struct {
	ExternalID string `json:"externalId"`
	AccountID  string `json:"accountId"`
	// CustomerID, when given, must be the account owner's (pre-provisioning).
	CustomerID string              `json:"customerId"`
	OrderItem  []tmf641ItemRequest `json:"orderItem"`
}
type tmf641CancelRequest struct {
	State string `json:"state"`
}

func tmf641SwapPair(items []tmf641ItemRequest) (int, bool) {
	if len(items) != 2 || items[0].Action != "delete" || items[1].Action != "add" {
		return 0, false
	}
	if roleOrDefault(items[0].Role) != roleOrDefault(items[1].Role) || strings.TrimSpace(items[1].OUISerial) == "" {
		return 0, false
	}
	return 1, true
}

// tmf641Delete ends the device in a delete item's role and closes the
// account's open sale in that role (a pre-provisioned CPE that never got
// to service). A role with no device is already what the delete asks for.
// An ouiSerial that names another device than the one in the role fails
// the item and ends nothing: the BSS's view is stale. expected, when set,
// is the mapping a resumed item recorded; another device in the role
// since then fails it the same way.
func (h *handler) tmf641Delete(ctx context.Context, req tmf641OrderRequest, item tmf641ItemRequest, expected *bss.AccountDeviceMapping) error {
	role := roleOrDefault(item.Role)
	active, err := h.mappings.ActiveDeviceForAccount(ctx, req.AccountID, role)
	switch {
	case errors.Is(err, bss.ErrNoDeviceForRole):
		active = nil
	case err != nil:
		return err
	}
	if active != nil {
		if expected != nil && active.ID != expected.ID {
			return errors.New("device assignment changed before delete recovery")
		}
		if want := strings.TrimSpace(item.OUISerial); want != "" && !sameDevice(active.OUISerial, want) {
			return fmt.Errorf("the %s of account %s is %s, not %s; nothing was ended", role, req.AccountID, active.OUISerial, want)
		}
		if err := h.mappings.UnassignDevice(ctx, req.AccountID, role, tmf641UnassignReason(item, false)); err != nil && !errors.Is(err, bss.ErrNoDeviceForRole) {
			return err
		}
	}
	return h.cancelOpenSales(ctx, req, item, role)
}

// cancelOpenSales cancels the account's open BSS sales in role (only the
// one for ouiSerial when the item names it), with an event each.
func (h *handler) cancelOpenSales(ctx context.Context, req tmf641OrderRequest, item tmf641ItemRequest, role string) error {
	if h.onboarding == nil {
		return nil
	}
	sales, err := h.onboarding.OpenBSSSales(ctx, req.AccountID, role)
	if err != nil {
		return err
	}
	for _, o := range sales {
		if want := strings.TrimSpace(item.OUISerial); want != "" && !sameDevice(o.ExpectedOUI+"+"+o.ExpectedSerial, want) {
			continue
		}
		reason := "deleted by service order " + req.ExternalID
		if err := h.onboarding.CancelWithReason(ctx, o.ID, reason); errors.Is(err, onboarding.ErrNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if err := h.auditor.Record(ctx, "bss:"+req.AccountID, "", "OnboardingCancelled", map[string]any{
			"onboarding_id": o.ID, "account_id": req.AccountID, "from": o.State, "to": onboarding.StateCancelled, "external_id": req.ExternalID,
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		after, err := h.onboarding.Get(ctx, o.ID)
		if err != nil {
			return err
		}
		h.publishSaleChange(ctx, after, o.State)
	}
	return nil
}

// sameDevice compares two device ids by OUI and serial, so the canonical
// form and the TR-069 Annex form (with or without ProductClass) match.
func sameDevice(a, b string) bool {
	ouiA, serialA := bss.ParseOUISerial(a)
	ouiB, serialB := bss.ParseOUISerial(b)
	return serialA != "" && serialA == serialB && strings.EqualFold(ouiA, ouiB)
}

// tmf641UnassignReason is the reason recorded when a delete or swap item
// ends an assignment. Without one, a swap records an upgrade and a delete a
// return.
func tmf641UnassignReason(item tmf641ItemRequest, swap bool) string {
	if reason := strings.TrimSpace(item.UnassignReason); reason != "" {
		return reason
	}
	if swap {
		return bss.ReasonUpgrade
	}
	return bss.ReasonReturn
}

// sameTMF641Order compares a stored order with a new request after both
// pass through the request type, so key order and fields the ACS ignores
// do not count as a difference.
func sameTMF641Order(stored json.RawMessage, req tmf641OrderRequest) bool {
	var prev tmf641OrderRequest
	if json.Unmarshal(stored, &prev) != nil {
		return false
	}
	a, _ := json.Marshal(prev)
	b, _ := json.Marshal(req)
	return bytes.Equal(a, b)
}

func (h *handler) createTMF641Order(w http.ResponseWriter, r *http.Request) {
	var req tmf641OrderRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.ExternalID) == "" || strings.TrimSpace(req.AccountID) == "" || len(req.OrderItem) == 0 {
		writeError(w, 400, "ErrInvalidRequest", "externalId, accountId, and orderItem are required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFExecute, req.AccountID) {
		return
	}
	if existing, err := h.mappings.FindServiceOrder(r.Context(), req.ExternalID); err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	} else if existing != nil {
		// externalId is an idempotency key, not an authority boundary. Never
		// let a tenant replay another tenant's key to retrieve its order.
		if existing.AccountID != req.AccountID {
			writeError(w, 404, "ErrNotFound", "no such service order")
			return
		}
		// The same externalId with another order is a client bug, not a
		// replay: answering with the first order would hide it.
		if !sameTMF641Order(existing.RawRequest, req) {
			writeError(w, http.StatusConflict, "ErrIdempotencyKeyReused", "this externalId was already used for a different order")
			return
		}
		items, _ := h.mappings.ServiceOrderItems(r.Context(), existing.ID)
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, 200, tmf641OrderResponse(existing, items))
		return
	}
	raw, _ := json.Marshal(req)
	items := make([]bss.ServiceOrderItem, len(req.OrderItem))
	for i, it := range req.OrderItem {
		if it.Action != "add" && it.Action != "modify" && it.Action != "delete" && it.Action != "noChange" {
			writeError(w, 400, "ErrInvalidRequest", "invalid orderItem action")
			return
		}
		items[i] = bss.ServiceOrderItem{Seq: i, Action: it.Action, Role: it.Role, Status: "PENDING"}
		if it.Action == "add" && strings.TrimSpace(it.OUISerial) == "" {
			writeError(w, 400, "ErrInvalidRequest", "ouiSerial is required for add items")
			return
		}
		if it.Action == "delete" && strings.TrimSpace(it.Role) == "" {
			writeError(w, 400, "ErrInvalidRequest", "role is required for delete items")
			return
		}
		if reason := strings.TrimSpace(it.UnassignReason); reason != "" && !bss.ValidUnassignReason(reason) {
			writeError(w, 400, "ErrInvalidRequest", "unassignReason must be one of rma, upgrade, return, moved, corrected")
			return
		}
	}
	order, err := h.mappings.CreateServiceOrder(r.Context(), req.ExternalID, req.AccountID, raw, items)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	for i := range items {
		if items[i].Action == "noChange" {
			if err := h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "COMPLETED", ""); err != nil {
				writeError(w, 500, "ErrInternal", "internal error")
				return
			}
			items[i].Status = "COMPLETED"
		}
	}
	swapAddIndex, isSwap := tmf641SwapPair(req.OrderItem)
	for i, item := range req.OrderItem {
		if isSwap && i == 0 {
			item = req.OrderItem[swapAddIndex]
			reason := tmf641UnassignReason(req.OrderItem[0], true)
			mapping, execErr := h.mappings.SwapDevice(r.Context(), req.AccountID, roleOrDefault(item.Role), item.OUISerial, reason)
			if execErr != nil {
				_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "FAILED", execErr.Error())
				items[i].Status = "FAILED"
				for j := i + 1; j < len(items); j++ {
					_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[j].ID, "SKIPPED", "previous item failed")
					items[j].Status = "SKIPPED"
				}
				break
			}
			items[0].Status = "COMPLETED"
			items[swapAddIndex].MappingID = mapping.ID
			items[swapAddIndex].Status = "COMPLETED"
			_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[0].ID, "COMPLETED", "")
			_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[swapAddIndex].ID, "COMPLETED", "")
			continue
		}
		if isSwap && i == swapAddIndex {
			continue
		}
		if i > 0 && items[i-1].Status == "FAILED" {
			_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "SKIPPED", "previous item failed")
			items[i].Status = "SKIPPED"
			continue
		}
		if item.Action == "add" {
			mapping, execErr := h.mappings.AssignDevice(r.Context(), req.AccountID, item.OUISerial, roleOrDefault(item.Role), item.ServicePlan)
			if errors.Is(execErr, bss.ErrDeviceNotFound) {
				// Sold before installation: wait for the CPE instead of
				// failing. Later items stay PENDING behind this one.
				if o, ppErr := h.preProvision(r.Context(), tmf641PreProvision(req, item)); ppErr == nil {
					if execErr = h.mappings.MarkItemAwaitingDevice(r.Context(), items[i].ID, o.ID); execErr == nil {
						items[i].Status = bss.ItemAwaitingDevice
						break
					}
				} else if !errors.Is(ppErr, errPreProvisionUnavailable) {
					execErr = ppErr
				}
			}
			if execErr != nil {
				_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "FAILED", execErr.Error())
				items[i].Status = "FAILED"
				for j := i + 1; j < len(items); j++ {
					_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[j].ID, "SKIPPED", "previous item failed")
					items[j].Status = "SKIPPED"
				}
				break
			}
			items[i].MappingID = mapping.ID
			items[i].Status = "COMPLETED"
			_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "COMPLETED", "")
		} else if item.Action == "delete" {
			if execErr := h.tmf641Delete(r.Context(), req, item, nil); execErr != nil {
				_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "FAILED", execErr.Error())
				items[i].Status = "FAILED"
				for j := i + 1; j < len(items); j++ {
					_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[j].ID, "SKIPPED", "previous item failed")
					items[j].Status = "SKIPPED"
				}
				break
			}
			items[i].Status = "COMPLETED"
			_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "COMPLETED", "")
		} else if item.Action == "modify" {
			mapping, lookupErr := h.mappings.ActiveDeviceForAccount(r.Context(), req.AccountID, roleOrDefault(item.Role))
			if lookupErr != nil {
				_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "FAILED", lookupErr.Error())
				items[i].Status = "FAILED"
				for j := i + 1; j < len(items); j++ {
					_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[j].ID, "SKIPPED", "previous item failed")
					items[j].Status = "SKIPPED"
				}
				break
			}
			params, translateErr := h.translateActionForDevice(r.Context(), "MODIFY_WIFI", item.Parameters, mapping.DeviceID)
			if translateErr == nil {
				translateErr = h.claimSecrets(r.Context(), r, item.Parameters, params, mapping.DeviceID, true)
			}
			if translateErr != nil {
				_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "FAILED", translateErr.Error())
				items[i].Status = "FAILED"
				for j := i + 1; j < len(items); j++ {
					_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[j].ID, "SKIPPED", "previous item failed")
					items[j].Status = "SKIPPED"
				}
				break
			}
			if _, dispatchErr := h.dispatchOrder(r.Context(), req.ExternalID+":"+strconv.Itoa(i), req.AccountID, "MODIFY_WIFI", mapping.DeviceID, params); dispatchErr != nil {
				_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "FAILED", dispatchErr.Error())
				items[i].Status = "FAILED"
				for j := i + 1; j < len(items); j++ {
					_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[j].ID, "SKIPPED", "previous item failed")
					items[j].Status = "SKIPPED"
				}
				break
			}
			items[i].Status = "DISPATCHED"
			_ = h.mappings.UpdateServiceOrderItem(r.Context(), items[i].ID, "DISPATCHED", "")
		}
	}
	writeJSON(w, 201, tmf641OrderResponse(order, items))
}

func tmf641PreProvision(req tmf641OrderRequest, item tmf641ItemRequest) preProvisionRequest {
	return preProvisionRequest{CustomerID: req.CustomerID, AccountID: req.AccountID, ExternalRef: req.ExternalID, OUISerial: item.OUISerial, Role: item.Role,
		ServicePlan: item.ServicePlan, ICCID: item.ICCID, IMSI: item.IMSI, Place: item.Place}
}

func tmf641OrderResponse(order *bss.ServiceOrder, items []bss.ServiceOrderItem) map[string]any {
	states := make([]string, len(items))
	out := make([]tmf.ServiceOrderItem, len(items))
	for i, it := range items {
		states[i] = it.Status
		out[i] = tmf.ServiceOrderItem{ID: it.ID, Action: it.Action, State: it.Status, Role: it.Role}
	}
	return map[string]any{"id": order.ID, "href": "/tmf-api/serviceOrdering/v4/serviceOrder/" + order.ID, "externalId": order.ExternalID, "state": tmf.ServiceOrderState(order.CancelledAt != nil, states), "orderDate": order.CreatedAt, "relatedParty": []tmf.RelatedParty{{ID: order.AccountID, Role: "customer"}}, "orderItem": out}
}

func (h *handler) getTMF641Order(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	order, err := h.mappings.FindServiceOrder(r.Context(), id)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if order == nil {
		writeError(w, 404, "ErrNotFound", "no such service order")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFRead, order.AccountID) {
		return
	}
	items, err := h.mappings.ServiceOrderItems(r.Context(), order.ID)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, 200, tmf641OrderResponse(order, items))
}

func (h *handler) listTMF641Orders(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		writeError(w, 400, "ErrInvalidRequest", "accountId query parameter is required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFRead, accountID) {
		return
	}
	orders, err := h.mappings.ListServiceOrders(r.Context(), accountID, 100)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(orders))
	for _, o := range orders {
		items, e := h.mappings.ServiceOrderItems(r.Context(), o.ID)
		if e != nil {
			writeError(w, 500, "ErrInternal", "internal error")
			return
		}
		out = append(out, tmf641OrderResponse(&o, items))
	}
	writeJSON(w, 200, out)
}

func (h *handler) cancelTMF641Order(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req tmf641CancelRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.State != "cancelled" {
		writeError(w, 400, "ErrInvalidRequest", "only state=cancelled is supported")
		return
	}
	order, err := h.mappings.FindServiceOrder(r.Context(), id)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if order == nil {
		writeError(w, 404, "ErrNotFound", "no such service order")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFExecute, order.AccountID) {
		return
	}
	if err := h.mappings.CancelServiceOrder(r.Context(), id); err != nil {
		writeError(w, 409, "ErrConflict", "service order cannot be cancelled after execution begins")
		return
	}
	order, err = h.mappings.FindServiceOrder(r.Context(), id)
	if err != nil || order == nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	items, err := h.mappings.ServiceOrderItems(r.Context(), order.ID)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, 200, tmf641OrderResponse(order, items))
}
