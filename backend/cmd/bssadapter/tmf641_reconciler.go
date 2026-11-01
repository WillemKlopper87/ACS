package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"acs/internal/bss"
)

const tmf641ReconcileInterval = 10 * time.Second

func (h *handler) runTMF641ReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(tmf641ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.reconcileTMF641Orders(ctx)
		}
	}
}

// reconcileTMF641Orders resumes items abandoned in PENDING or an expired
// RUNNING lease. The original request is stored with the parent order; stable
// operation IDs and checks against current mappings make replay safe.
func (h *handler) reconcileTMF641Orders(ctx context.Context) {
	work, err := h.mappings.ClaimDueServiceOrderItems(ctx, 25)
	if err != nil {
		h.logger.Error("failed to claim TMF641 items for recovery", "err", err)
		return
	}
	for _, item := range work {
		if err := h.resumeTMF641Item(ctx, item); err != nil {
			h.logger.Error("TMF641 item recovery failed", "err", err, "external_id", item.Order.ExternalID, "seq", item.Item.Seq)
			// Keep the RUNNING claim and its timestamp. The ten-minute lease
			// limits retries during sustained database or ACS outages.
		}
	}
}

func (h *handler) resumeTMF641Item(ctx context.Context, work bss.ServiceOrderWork) error {
	var req tmf641OrderRequest
	if err := json.Unmarshal(work.Order.RawRequest, &req); err != nil {
		return h.finishTMF641Item(ctx, work.Item, "FAILED", "stored service order request is invalid")
	}
	if work.Item.Seq < 0 || work.Item.Seq >= len(req.OrderItem) {
		return h.finishTMF641Item(ctx, work.Item, "FAILED", "stored service order item index is invalid")
	}
	item := req.OrderItem[work.Item.Seq]
	role := roleOrDefault(item.Role)

	if swapAddIndex, isSwap := tmf641SwapPair(req.OrderItem); isSwap && work.Item.Seq == 0 {
		add := req.OrderItem[swapAddIndex]
		active, err := h.mappings.ActiveDeviceForAccount(ctx, req.AccountID, roleOrDefault(add.Role))
		if err == nil && active.OUISerial == strings.TrimSpace(add.OUISerial) {
			if err := h.mappings.SetServiceOrderItemMapping(ctx, work.Item.ID, active.ID); err != nil {
				return err
			}
			if err := h.finishTMF641Item(ctx, work.Item, "COMPLETED", ""); err != nil {
				return err
			}
			return h.finishTMF641Sibling(ctx, work.Order.ID, swapAddIndex, active.ID, "COMPLETED", "")
		}
		reason := tmf641UnassignReason(item, true)
		mapping, err := h.mappings.SwapDevice(ctx, req.AccountID, roleOrDefault(add.Role), add.OUISerial, reason)
		if err != nil {
			return h.finishTMF641Item(ctx, work.Item, "FAILED", err.Error())
		}
		if err := h.mappings.SetServiceOrderItemMapping(ctx, work.Item.ID, mapping.ID); err != nil {
			return err
		}
		if err := h.finishTMF641Item(ctx, work.Item, "COMPLETED", ""); err != nil {
			return err
		}
		return h.finishTMF641Sibling(ctx, work.Order.ID, swapAddIndex, mapping.ID, "COMPLETED", "")
	}

	switch item.Action {
	case "noChange":
		return h.finishTMF641Item(ctx, work.Item, "COMPLETED", "")
	case "add":
		deviceID, err := h.mappings.DeviceIDForSerial(ctx, item.OUISerial)
		if errors.Is(err, bss.ErrDeviceNotFound) {
			o, ppErr := h.preProvision(ctx, tmf641PreProvision(req, item))
			if ppErr == nil {
				return h.mappings.MarkItemAwaitingDevice(ctx, work.Item.ID, o.ID)
			}
			if !errors.Is(ppErr, errPreProvisionUnavailable) {
				err = ppErr
			}
		}
		if err != nil {
			return h.finishTMF641Item(ctx, work.Item, "FAILED", err.Error())
		}
		active, activeErr := h.mappings.ActiveDeviceForAccount(ctx, req.AccountID, role)
		if activeErr == nil && active.DeviceID == deviceID {
			if err := h.mappings.SetServiceOrderItemMapping(ctx, work.Item.ID, active.ID); err != nil {
				return err
			}
			return h.finishTMF641Item(ctx, work.Item, "COMPLETED", "")
		}
		mapping, err := h.mappings.AssignDevice(ctx, req.AccountID, item.OUISerial, role, item.ServicePlan)
		if err != nil {
			return h.finishTMF641Item(ctx, work.Item, "FAILED", err.Error())
		}
		if err := h.mappings.SetServiceOrderItemMapping(ctx, work.Item.ID, mapping.ID); err != nil {
			return err
		}
		return h.finishTMF641Item(ctx, work.Item, "COMPLETED", "")
	case "delete":
		return h.resumeTMF641Delete(ctx, work, req, item, role)
	case "modify":
		mapping, err := h.mappings.ActiveDeviceForAccount(ctx, req.AccountID, role)
		if err != nil {
			return h.finishTMF641Item(ctx, work.Item, "FAILED", err.Error())
		}
		params, err := h.translateActionForDevice(ctx, "MODIFY_WIFI", item.Parameters, mapping.DeviceID)
		if err != nil {
			return h.finishTMF641Item(ctx, work.Item, "FAILED", err.Error())
		}
		externalID := req.ExternalID + ":" + itoa(work.Item.Seq)
		_, err = h.dispatchOrder(ctx, externalID, req.AccountID, "MODIFY_WIFI", mapping.DeviceID, params)
		if err != nil && !errors.Is(err, bss.ErrOrderAlreadyExists) {
			// dispatchOrder has already durably written a PENDING_DISPATCH row
			// for transient ACS failures; its outbox owns retry from here.
			order, lookupErr := h.mappings.FindOrder(ctx, externalID)
			if lookupErr != nil {
				return lookupErr
			}
			if order == nil || order.Status == bss.OrderStatusDeadLettered {
				return h.finishTMF641Item(ctx, work.Item, "FAILED", err.Error())
			}
		}
		if errors.Is(err, bss.ErrOrderAlreadyExists) {
			order, lookupErr := h.mappings.FindOrder(ctx, externalID)
			if lookupErr != nil {
				return lookupErr
			}
			if order == nil || order.Status == bss.OrderStatusDeadLettered {
				return h.finishTMF641Item(ctx, work.Item, "FAILED", "parameter dispatch previously failed")
			}
		}
		return h.finishTMF641Item(ctx, work.Item, "DISPATCHED", "")
	default:
		return h.finishTMF641Item(ctx, work.Item, "FAILED", "unsupported stored service order action")
	}
}

func (h *handler) resumeTMF641Delete(ctx context.Context, work bss.ServiceOrderWork, req tmf641OrderRequest, item tmf641ItemRequest, role string) error {
	var expected *bss.AccountDeviceMapping
	if work.Item.MappingID != "" {
		mapping, err := h.mappings.GetMappingByID(ctx, work.Item.MappingID)
		if err != nil {
			return err
		}
		if mapping.UnassignedAt != nil {
			if err := h.cancelOpenSales(ctx, req, item, role); err != nil {
				return err
			}
			return h.finishTMF641Item(ctx, work.Item, "COMPLETED", "")
		}
		expected = mapping
	} else if active, err := h.mappings.ActiveDeviceForAccount(ctx, req.AccountID, role); err == nil {
		if err := h.mappings.SetServiceOrderItemMapping(ctx, work.Item.ID, active.ID); err != nil {
			return err
		}
	}
	if err := h.tmf641Delete(ctx, req, item, expected); err != nil {
		return h.finishTMF641Item(ctx, work.Item, "FAILED", err.Error())
	}
	return h.finishTMF641Item(ctx, work.Item, "COMPLETED", "")
}

func (h *handler) finishTMF641Item(ctx context.Context, item bss.ServiceOrderItem, status, message string) error {
	if err := h.mappings.UpdateServiceOrderItem(ctx, item.ID, status, message); err != nil {
		return err
	}
	if status == "FAILED" {
		return h.skipTMF641PendingItems(ctx, item.OrderID, item.Seq)
	}
	return nil
}

func (h *handler) finishTMF641Sibling(ctx context.Context, orderID string, seq int, mappingID, status, message string) error {
	items, err := h.mappings.ServiceOrderItems(ctx, orderID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Seq == seq {
			if mappingID != "" {
				if err := h.mappings.SetServiceOrderItemMapping(ctx, item.ID, mappingID); err != nil {
					return err
				}
			}
			return h.mappings.UpdateServiceOrderItem(ctx, item.ID, status, message)
		}
	}
	return errors.New("TMF641 sibling item not found")
}

func (h *handler) skipTMF641PendingItems(ctx context.Context, orderID string, afterSeq int) error {
	items, err := h.mappings.ServiceOrderItems(ctx, orderID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Seq > afterSeq && (item.Status == "PENDING" || item.Status == "RUNNING") {
			if err := h.mappings.UpdateServiceOrderItem(ctx, item.ID, "SKIPPED", "previous item failed"); err != nil {
				return err
			}
		}
	}
	return nil
}

func itoa(value int) string { return strconv.Itoa(value) }
