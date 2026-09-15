package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"acs/internal/bss"
	"acs/internal/tmf"
)

type tmf641ItemRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Role   string `json:"role"`
}
type tmf641OrderRequest struct {
	ExternalID string              `json:"externalId"`
	AccountID  string              `json:"accountId"`
	OrderItem  []tmf641ItemRequest `json:"orderItem"`
}
type tmf641CancelRequest struct {
	State string `json:"state"`
}

func (h *handler) createTMF641Order(w http.ResponseWriter, r *http.Request) {
	var req tmf641OrderRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.ExternalID) == "" || strings.TrimSpace(req.AccountID) == "" || len(req.OrderItem) == 0 {
		writeError(w, 400, "ErrInvalidRequest", "externalId, accountId, and orderItem are required")
		return
	}
	if existing, err := h.mappings.FindServiceOrder(r.Context(), req.ExternalID); err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	} else if existing != nil {
		items, _ := h.mappings.ServiceOrderItems(r.Context(), existing.ID)
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
	writeJSON(w, 201, tmf641OrderResponse(order, items))
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
	items, err := h.mappings.ServiceOrderItems(r.Context(), order.ID)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, 200, tmf641OrderResponse(order, items))
}

func (h *handler) listTMF641Orders(w http.ResponseWriter, r *http.Request) {
	orders, err := h.mappings.ListServiceOrders(r.Context(), strings.TrimSpace(r.URL.Query().Get("accountId")), 100)
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
	if err := h.mappings.CancelServiceOrder(r.Context(), id); err != nil {
		writeError(w, 409, "ErrConflict", "service order cannot be cancelled after execution begins")
		return
	}
	h.getTMF641Order(w, r)
}
