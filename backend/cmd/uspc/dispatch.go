// dispatch.go maps internal/jobs' queued job types onto USP (TR-369)
// request messages (design S6.2). Nine of internal/jobs' fifteen types
// have a real USP equivalent buildUSPRequest can actually render; the
// other six are FIRMWARE_DOWNLOAD (has a case below, but always returns
// ErrUnsupportedOverUSP -- see that case's own doc comment for why, and
// dispatcher.go's uspDispatchableTypes for why it's deliberately excluded
// from what gets leased at all) plus five CWMP-only concepts with no USP
// counterpart at all (CONNECTION_REQUEST, SCHEDULE_INFORM,
// SET_PARAMETER_ATTRIBUTES, GET_PARAMETER_ATTRIBUTES, UPLOAD), never
// leased over this transport.
//
// This file only builds request bytes; it does not touch
// internal/jobs.Repository (leasing, status transitions) -- that is a
// later task's dispatcher, which decides what to do with the returned
// bytes (wrap in a Record via usp.EncodeRecord and send) or with
// ErrUnsupportedOverUSP (leave the job QUEUED for a human, don't fail
// it, don't re-lease it in the same sweep).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"acs/internal/jobs"
	"acs/internal/usp"
)

// ErrUnsupportedOverUSP is returned by buildUSPRequest for a job type
// that has no USP mapping -- either one of the five CWMP-only types, a
// type whose USP command shape isn't implemented yet (FIRMWARE_DOWNLOAD;
// see its case below), or any future/unrecognized job.Type value. The
// caller (Task 4's dispatcher) treats this as "put the job back, do not
// mark it failed" rather than as a device-side rejection.
var ErrUnsupportedOverUSP = errors.New("job type has no USP mapping")

// buildUSPRequest maps one queued job onto the USP request message that
// carries it out, returning the Msg-encoded bytes usp.EncodeRecord still
// needs to wrap in a Record at the call site (Task 4). msgID is the
// caller-supplied correlation id (usp.NewMsgID()).
func buildUSPRequest(msgID string, job *jobs.Job) ([]byte, error) {
	switch job.Type {
	case jobs.TypeGetParameter:
		var payload jobs.GetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal GET_PARAMETER payload: %w", err)
		}
		// maxDepth 0 = unlimited, matching a CWMP GetParameterValues'
		// unrestricted depth.
		return usp.EncodeGet(msgID, payload.Paths, 0)

	case jobs.TypeSetParameter:
		var payload jobs.SetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal SET_PARAMETER payload: %w", err)
		}
		// allowPartial=false: a half-applied Set leaves a device in a
		// state no operator asked for.
		return usp.EncodeSet(msgID, false, groupParametersByObjectPath(payload.Parameters))

	case jobs.TypeAddObject:
		var payload jobs.AddObjectPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal ADD_OBJECT payload: %w", err)
		}
		// Always a non-nil map (even when Parameters is nil/empty) --
		// EncodeAdd ranges over it either way, but building it
		// unconditionally here means there's no ambiguity to check at
		// the call site.
		params := make(map[string]string, len(payload.Parameters))
		for _, p := range payload.Parameters {
			params[p.Name] = p.Value
		}
		return usp.EncodeAdd(msgID, true, payload.ObjectPath, params)

	case jobs.TypeDeleteObject:
		var payload jobs.DeleteObjectPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal DELETE_OBJECT payload: %w", err)
		}
		return usp.EncodeDelete(msgID, true, []string{payload.ObjectPath})

	case jobs.TypeReboot:
		// Cause passed explicitly rather than relying on the device's
		// own default (which the spec says is the Remote* variant
		// anyway) -- an operator-triggered job genuinely is a remote
		// cause, and being explicit here is clearer than depending on
		// an implicit default.
		return usp.EncodeOperate(msgID, "Device.Reboot()", job.CommandKey, true, map[string]string{"Cause": "RemoteReboot"})

	case jobs.TypeFactoryReset:
		return usp.EncodeOperate(msgID, "Device.FactoryReset()", job.CommandKey, true, map[string]string{"Cause": "RemoteFactoryReset"})

	case jobs.TypeDiagnosticsPing:
		var payload jobs.DiagnosticsPingPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal DIAGNOSTICS_PING payload: %w", err)
		}
		// Device.IP.Diagnostics.IPPing()'s InputArguments (TR-181
		// 2.18.1 USP data model XML): Interface (optional, a reference
		// path -- left empty, no payload field carries it),
		// ProtocolVersion (mandatory enum Any/IPv4/IPv6 -- no payload
		// field, hardcoded to "Any"), Host, NumberOfRepetitions,
		// Timeout, DataBlockSize (all mandatory), DSCP (optional).
		// DiagnosticsPingPayload.Prefix has no home here -- it's a
		// CWMP IPv6-prefix extension with no USP equivalent field.
		inputArgs := map[string]string{
			"ProtocolVersion":     "Any",
			"Host":                payload.Host,
			"NumberOfRepetitions": strconv.Itoa(payload.NumberOfRepetitions),
			"Timeout":             strconv.Itoa(payload.Timeout),
			"DataBlockSize":       strconv.Itoa(payload.DataBlockSize),
			"DSCP":                strconv.Itoa(payload.DSCP),
		}
		return usp.EncodeOperate(msgID, "Device.IP.Diagnostics.IPPing()", job.CommandKey, true, inputArgs)

	case jobs.TypeDiagnosticsTraceroute:
		var payload jobs.DiagnosticsTraceroutePayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal DIAGNOSTICS_TRACEROUTE payload: %w", err)
		}
		// Device.IP.Diagnostics.TraceRoute()'s InputArguments (same
		// source as IPPing above): Interface (optional, omitted),
		// ProtocolVersion (mandatory, hardcoded "Any" as above), Host
		// (mandatory), NumberOfTries, Timeout, DataBlockSize, DSCP,
		// MaxHopCount (all optional). Prefix again has no home here.
		inputArgs := map[string]string{
			"ProtocolVersion": "Any",
			"Host":            payload.Host,
			"NumberOfTries":   strconv.Itoa(payload.NumberOfTries),
			"Timeout":         strconv.Itoa(payload.Timeout),
			"DataBlockSize":   strconv.Itoa(payload.DataBlockSize),
			"DSCP":            strconv.Itoa(payload.DSCP),
			"MaxHopCount":     strconv.Itoa(payload.MaxHopCount),
		}
		return usp.EncodeOperate(msgID, "Device.IP.Diagnostics.TraceRoute()", job.CommandKey, true, inputArgs)

	case jobs.TypeParameterDiscovery:
		var payload jobs.ParameterDiscoveryPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal PARAMETER_DISCOVERY payload: %w", err)
		}
		// payload.Root carries the real root to discover (mirroring
		// CWMP's own data-model-root fallback pattern), not a
		// hardcoded "Device.". FallbackRoot/IsFallback are for a
		// caller-side retry-with-fallback pattern that belongs to the
		// dispatcher (a later task), not here.
		return usp.EncodeGetSupportedDM(msgID, []string{payload.Root}, false, true, true, true)

	case jobs.TypeFirmwareDownload:
		// Device.FirmwareImage.{i}.Download()'s InputArguments are
		// known with reasonable confidence (URL mandatory,
		// AutoActivate mandatory boolean, Username/Password/FileSize/
		// CheckSumAlgorithm/CheckSum optional -- TR-181 2.18.1 USP data
		// model XML), but Device.FirmwareImage is a multi-instance
		// table (typically one active + one standby slot on a real
		// device): sending Download() requires first knowing which
		// instance to target, which means a discovery round-trip
		// (GetInstances or Get on "Device.FirmwareImage.") this plan
		// does not build. Not a confidence gap on the arguments --
		// purely the missing instance-selection step. A later plan can
		// add the discovery step and revisit this.
		return nil, ErrUnsupportedOverUSP

	default:
		// The five CWMP-only types (CONNECTION_REQUEST, SCHEDULE_INFORM,
		// SET_PARAMETER_ATTRIBUTES, GET_PARAMETER_ATTRIBUTES, UPLOAD)
		// and any future/unrecognized job.Type land here rather than
		// panicking.
		return nil, ErrUnsupportedOverUSP
	}
}

// groupParametersByObjectPath groups a flat list of full-path parameter
// writes (jobs.ParameterWrite.Name, e.g. "Device.WiFi.SSID.1.SSID") into
// the per-object-path, per-leaf-name shape usp.EncodeSet expects: an
// object path (including its trailing ".") mapped to {leaf name ->
// value}, matching how USP's Set.UpdateObjs/ParamSettings addresses a
// parameter relative to its object rather than by full path.
//
// A name with no dot at all has no real TR-181 precedent -- every data
// model parameter lives at least two segments below the root (e.g.
// "Device.DeviceInfo.SerialNumber") -- but is still handled rather than
// panicking: it groups under the empty-string object path with the
// whole name as the leaf.
func groupParametersByObjectPath(params []jobs.ParameterWrite) map[string]map[string]string {
	grouped := make(map[string]map[string]string)
	for _, p := range params {
		idx := strings.LastIndex(p.Name, ".")
		objPath := p.Name[:idx+1]
		leaf := p.Name[idx+1:]
		if grouped[objPath] == nil {
			grouped[objPath] = make(map[string]string)
		}
		grouped[objPath][leaf] = p.Value
	}
	return grouped
}
