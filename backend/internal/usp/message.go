package usp

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"acs/internal/usp/uspproto"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// ErrMalformedMessage is returned when an inbound payload is not a
// decodable USP Msg.
var ErrMalformedMessage = errors.New("malformed USP message")

// ErrNotOnBoardRequest is returned when a Notify message is not an
// OnBoardRequest notification.
var ErrNotOnBoardRequest = errors.New("USP message is not an OnBoardRequest Notify")

// ErrNotOperationComplete is returned when a Notify message is not an
// OperationComplete notification.
var ErrNotOperationComplete = errors.New("USP message is not an OperationComplete Notify")

// NewMsgID returns a fresh message correlation id. USP requires msg_id
// to be unique per outstanding request from a given endpoint; a UUID is
// the cheapest way to guarantee that without shared state.
func NewMsgID() string { return uuid.NewString() }

// encodeRequest marshals one request into a Msg with the given header.
func encodeRequest(msgID string, msgType uspproto.Header_MsgType, req *uspproto.Request) ([]byte, error) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: msgType},
		Body:   &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: req}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal USP %v: %w", msgType, err)
	}
	return wire, nil
}

// EncodeGet builds a Get. maxDepth 0 means unlimited.
func EncodeGet(msgID string, paths []string, maxDepth uint32) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET, &uspproto.Request{
		ReqType: &uspproto.Request_Get{Get: &uspproto.Get{
			ParamPaths: paths,
			MaxDepth:   maxDepth,
		}},
	})
}

// EncodeSet builds a Set. updates maps an object path to the parameters
// being written on it.
//
// allowPartial false means the agent applies all of it or none, which is
// what a configuration write generally wants: a half-applied Set leaves
// a device in a state no operator asked for.
//
// Every parameter is encoded with Required: true, which tells the agent
// to fail the entire Set if that single parameter fails, regardless of
// allowPartial. A per-parameter required flag -- for a caller that wants
// a best-effort write where some parameters may legitimately fail -- is
// a later plan's concern; this function offers no way to request it.
func EncodeSet(msgID string, allowPartial bool, updates map[string]map[string]string) ([]byte, error) {
	objs := make([]*uspproto.Set_UpdateObject, 0, len(updates))
	for _, objPath := range slices.Sorted(maps.Keys(updates)) {
		params := updates[objPath]
		settings := make([]*uspproto.Set_UpdateParamSetting, 0, len(params))
		for _, name := range slices.Sorted(maps.Keys(params)) {
			settings = append(settings, &uspproto.Set_UpdateParamSetting{
				Param:    name,
				Value:    params[name],
				Required: true,
			})
		}
		objs = append(objs, &uspproto.Set_UpdateObject{
			ObjPath:       objPath,
			ParamSettings: settings,
		})
	}
	return encodeRequest(msgID, uspproto.Header_SET, &uspproto.Request{
		ReqType: &uspproto.Request_Set{Set: &uspproto.Set{
			AllowPartial: allowPartial,
			UpdateObjs:   objs,
		}},
	})
}

// EncodeAdd builds an Add creating one instance of a multi-instance
// object, with optional initial parameter values.
//
// As with EncodeSet, every parameter is encoded with Required: true, so
// the agent fails the whole Add if any single initial value is
// rejected. A per-parameter required flag is a later plan's concern if
// a best-effort creation is ever needed.
func EncodeAdd(msgID string, allowPartial bool, objPath string, params map[string]string) ([]byte, error) {
	settings := make([]*uspproto.Add_CreateParamSetting, 0, len(params))
	for _, name := range slices.Sorted(maps.Keys(params)) {
		settings = append(settings, &uspproto.Add_CreateParamSetting{
			Param:    name,
			Value:    params[name],
			Required: true,
		})
	}
	return encodeRequest(msgID, uspproto.Header_ADD, &uspproto.Request{
		ReqType: &uspproto.Request_Add{Add: &uspproto.Add{
			AllowPartial: allowPartial,
			CreateObjs: []*uspproto.Add_CreateObject{{
				ObjPath:       objPath,
				ParamSettings: settings,
			}},
		}},
	})
}

// EncodeDelete builds a Delete over one or more object instances.
func EncodeDelete(msgID string, allowPartial bool, objPaths []string) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_DELETE, &uspproto.Request{
		ReqType: &uspproto.Request_Delete{Delete: &uspproto.Delete{
			AllowPartial: allowPartial,
			ObjPaths:     objPaths,
		}},
	})
}

// EncodeOperate builds an Operate invoking a data-model command.
//
// commandKey is the caller's correlation token, echoed back in
// Notify.OperationComplete for an asynchronous command. It is supplied
// rather than generated here because it must match the job the operation
// belongs to -- the same field CWMP's TransferComplete already
// correlates on (design §3.3, §6.3).
func EncodeOperate(msgID, command, commandKey string, sendResp bool, inputArgs map[string]string) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_OPERATE, &uspproto.Request{
		ReqType: &uspproto.Request_Operate{Operate: &uspproto.Operate{
			Command:    command,
			CommandKey: commandKey,
			SendResp:   sendResp,
			InputArgs:  inputArgs,
		}},
	})
}

// EncodeGetSupportedDM builds a GetSupportedDM, the message that reports
// each parameter's access type and each command's synchronicity -- so
// writability and sync/async are discoverable rather than guessed
// (design §3.3).
func EncodeGetSupportedDM(msgID string, objPaths []string, firstLevelOnly, returnCommands, returnEvents, returnParams bool) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET_SUPPORTED_DM, &uspproto.Request{
		ReqType: &uspproto.Request_GetSupportedDm{GetSupportedDm: &uspproto.GetSupportedDM{
			ObjPaths:       objPaths,
			FirstLevelOnly: firstLevelOnly,
			ReturnCommands: returnCommands,
			ReturnEvents:   returnEvents,
			ReturnParams:   returnParams,
		}},
	})
}

// EncodeGetInstances builds a GetInstances.
func EncodeGetInstances(msgID string, objPaths []string, firstLevelOnly bool) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET_INSTANCES, &uspproto.Request{
		ReqType: &uspproto.Request_GetInstances{GetInstances: &uspproto.GetInstances{
			ObjPaths:       objPaths,
			FirstLevelOnly: firstLevelOnly,
		}},
	})
}

// EncodeGetSupportedProtocol builds a GetSupportedProtocol advertising
// the versions this controller speaks.
func EncodeGetSupportedProtocol(msgID, controllerSupportedProtocolVersions string) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET_SUPPORTED_PROTO, &uspproto.Request{
		ReqType: &uspproto.Request_GetSupportedProtocol{GetSupportedProtocol: &uspproto.GetSupportedProtocol{
			ControllerSupportedProtocolVersions: controllerSupportedProtocolVersions,
		}},
	})
}

// DecodeMsg unmarshals a record payload into a Msg.
func DecodeMsg(payload []byte) (*uspproto.Msg, error) {
	var msg uspproto.Msg
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedMessage, err)
	}
	if msg.GetHeader() == nil || msg.GetBody() == nil {
		return nil, fmt.Errorf("%w: header or body absent", ErrMalformedMessage)
	}
	return &msg, nil
}

// OnBoardRequest represents a decoded OnBoardRequest Notify message,
// carrying the device's identity and supported protocol versions for
// initial reconciliation (design spec S5.3).
type OnBoardRequest struct {
	SubscriptionID                 string
	SendResp                       bool
	OUI                            string
	ProductClass                   string
	SerialNumber                   string
	AgentSupportedProtocolVersions string
}

// DecodeOnBoardRequest unmarshals a Notify message into an OnBoardRequest,
// returning ErrNotOnBoardRequest if the message is not a NOTIFY whose
// Notification oneof is an OnBoardRequest. This is deliberately narrow --
// only the one Notify variant this plan's reconciliation needs; the other
// five variants (ValueChange, ObjCreation, ObjDeletion, OperComplete, Event)
// are a later plan's concern.
func DecodeOnBoardRequest(msg *uspproto.Msg) (*OnBoardRequest, error) {
	if msg.GetHeader().GetMsgType() != uspproto.Header_NOTIFY {
		return nil, ErrNotOnBoardRequest
	}

	notify := msg.GetBody().GetRequest().GetNotify()
	if notify == nil {
		return nil, ErrNotOnBoardRequest
	}

	onBoardReqWrapper, ok := notify.GetNotification().(*uspproto.Notify_OnBoardReq)
	if !ok {
		return nil, ErrNotOnBoardRequest
	}

	obr := onBoardReqWrapper.OnBoardReq
	if obr == nil {
		return nil, ErrNotOnBoardRequest
	}

	return &OnBoardRequest{
		SubscriptionID:                 notify.GetSubscriptionId(),
		SendResp:                       notify.GetSendResp(),
		OUI:                            obr.GetOui(),
		ProductClass:                   obr.GetProductClass(),
		SerialNumber:                   obr.GetSerialNumber(),
		AgentSupportedProtocolVersions: obr.GetAgentSupportedProtocolVersions(),
	}, nil
}

// OperationComplete represents a decoded OperationComplete Notify
// message -- the async completion signal for an Operate command that
// wasn't (or in addition to being) answered synchronously via
// SendResp/OperateResp, correlated back to the job that requested it by
// CommandKey (the same field CWMP's TransferComplete already correlates
// on, design S6.3). OutputArgs is nil when the operation itself failed
// on the agent (Failed true); ErrCode/ErrMsg are only meaningful when
// Failed is true.
type OperationComplete struct {
	SubscriptionID string
	SendResp       bool
	ObjPath        string
	CommandName    string
	CommandKey     string
	OutputArgs     map[string]string
	Failed         bool
	ErrCode        uint32
	ErrMsg         string
}

// DecodeOperationComplete unmarshals a Notify message into an
// OperationComplete, returning ErrNotOperationComplete if the message is
// not a NOTIFY whose Notification oneof is an OperComplete. Mirrors
// DecodeOnBoardRequest's exact shape -- see its own doc comment for why
// this stays narrow to the one Notify variant a caller asked for.
//
// oc.GetOperationResp() is itself a oneof and protobuf allows it to be
// unset (nil) even though the spec doesn't intend that in practice; that
// case is treated the same as a successful operation with no output
// arguments (Failed false, OutputArgs nil) rather than panicking.
func DecodeOperationComplete(msg *uspproto.Msg) (*OperationComplete, error) {
	if msg.GetHeader().GetMsgType() != uspproto.Header_NOTIFY {
		return nil, ErrNotOperationComplete
	}

	notify := msg.GetBody().GetRequest().GetNotify()
	if notify == nil {
		return nil, ErrNotOperationComplete
	}

	operCompleteWrapper, ok := notify.GetNotification().(*uspproto.Notify_OperComplete)
	if !ok {
		return nil, ErrNotOperationComplete
	}

	oc := operCompleteWrapper.OperComplete
	if oc == nil {
		return nil, ErrNotOperationComplete
	}

	result := &OperationComplete{
		SubscriptionID: notify.GetSubscriptionId(),
		SendResp:       notify.GetSendResp(),
		ObjPath:        oc.GetObjPath(),
		CommandName:    oc.GetCommandName(),
		CommandKey:     oc.GetCommandKey(),
	}

	switch resp := oc.GetOperationResp().(type) {
	case *uspproto.Notify_OperationComplete_ReqOutputArgs:
		result.OutputArgs = resp.ReqOutputArgs.GetOutputArgs()
	case *uspproto.Notify_OperationComplete_CmdFailure:
		result.Failed = true
		result.ErrCode = resp.CmdFailure.GetErrCode()
		result.ErrMsg = resp.CmdFailure.GetErrMsg()
	}

	return result, nil
}

// EncodeNotifyResp builds a NotifyResp message, the acknowledgment sent
// by a controller when it receives an unsolicited Notify from an agent.
// It mirrors encodeRequest's shape but builds a Body_Response instead.
func EncodeNotifyResp(msgID, subscriptionID string) ([]byte, error) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_NOTIFY_RESP},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Response{
				Response: &uspproto.Response{
					RespType: &uspproto.Response_NotifyResp{
						NotifyResp: &uspproto.NotifyResp{
							SubscriptionId: subscriptionID,
						},
					},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal USP %v: %w", uspproto.Header_NOTIFY_RESP, err)
	}
	return wire, nil
}
