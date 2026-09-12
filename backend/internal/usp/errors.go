package usp

import (
	"errors"
	"fmt"

	"acs/internal/usp/uspproto"
)

// ErrorCode is a USP error code. The table runs 7000-7027 (design §3.3).
type ErrorCode uint32

const (
	ErrCodeMessageFailed        ErrorCode = 7000
	ErrCodeMessageNotSupported  ErrorCode = 7001
	ErrCodeRequestDenied        ErrorCode = 7002
	ErrCodeInternalError        ErrorCode = 7003
	ErrCodeInvalidArguments     ErrorCode = 7004
	ErrCodeResourcesExceeded    ErrorCode = 7005
	ErrCodePermissionDenied     ErrorCode = 7006
	ErrCodeInvalidConfiguration ErrorCode = 7007
	ErrCodeInvalidPathSyntax    ErrorCode = 7008
	ErrCodeParamActionFailed    ErrorCode = 7009
	ErrCodeUnsupportedParam     ErrorCode = 7010
	ErrCodeInvalidType          ErrorCode = 7011
	ErrCodeInvalidValue         ErrorCode = 7012
	ErrCodeNotWriteable         ErrorCode = 7013
	ErrCodeValueConflict        ErrorCode = 7014
	ErrCodeOperationError       ErrorCode = 7015
	ErrCodeObjectDoesNotExist   ErrorCode = 7016
	ErrCodeObjectNotCreatable   ErrorCode = 7017
	ErrCodeNotATable            ErrorCode = 7018
	ErrCodeObjectNotCreatableNC ErrorCode = 7019
	ErrCodeObjectNotUpdatable   ErrorCode = 7020
	ErrCodeRequiredParamFailed  ErrorCode = 7021
	ErrCodeCommandFailure       ErrorCode = 7022
	ErrCodeCommandCanceled      ErrorCode = 7023
	ErrCodeDeleteFailure        ErrorCode = 7024
	ErrCodeDuplicateKey         ErrorCode = 7025
	ErrCodeInvalidPath          ErrorCode = 7026
	ErrCodeInvalidCommandArgs   ErrorCode = 7027
)

var errorCodeText = map[ErrorCode]string{
	ErrCodeMessageFailed:        "message failed",
	ErrCodeMessageNotSupported:  "message not supported",
	ErrCodeRequestDenied:        "request denied",
	ErrCodeInternalError:        "internal error",
	ErrCodeInvalidArguments:     "invalid arguments",
	ErrCodeResourcesExceeded:    "resources exceeded",
	ErrCodePermissionDenied:     "permission denied",
	ErrCodeInvalidConfiguration: "invalid configuration",
	ErrCodeInvalidPathSyntax:    "invalid path syntax",
	ErrCodeParamActionFailed:    "parameter action failed",
	ErrCodeUnsupportedParam:     "unsupported parameter",
	ErrCodeInvalidType:          "invalid type",
	ErrCodeInvalidValue:         "invalid value",
	ErrCodeNotWriteable:         "attempt to update non-writeable parameter",
	ErrCodeValueConflict:        "value conflict",
	ErrCodeOperationError:       "operation error",
	ErrCodeObjectDoesNotExist:   "object does not exist",
	ErrCodeObjectNotCreatable:   "object could not be created",
	ErrCodeNotATable:            "object is not a table",
	ErrCodeObjectNotCreatableNC: "attempt to create non-creatable object",
	ErrCodeObjectNotUpdatable:   "object could not be updated",
	ErrCodeRequiredParamFailed:  "required parameter failed",
	ErrCodeCommandFailure:       "command failure",
	ErrCodeCommandCanceled:      "command canceled",
	ErrCodeDeleteFailure:        "delete failure",
	ErrCodeDuplicateKey:         "object exists with duplicate key",
	ErrCodeInvalidPath:          "invalid path",
	ErrCodeInvalidCommandArgs:   "invalid command arguments",
}

// String renders a code's meaning, falling back to the number so an
// unmapped code from a future USP version is still legible in a log or a
// job's failure detail.
func (c ErrorCode) String() string {
	if text, ok := errorCodeText[c]; ok {
		return text
	}
	return fmt.Sprintf("USP error %d", uint32(c))
}

// Sentinels for the codes later plans branch on (design §6.4). Match
// them with errors.Is rather than comparing integers at the call site.
//
// ErrNotWriteable is worth singling out: on CWMP a non-writeable
// parameter is the Huawei-class trap that resolves, sends and silently
// fails. On USP it is a typed error, which is a real improvement in
// diagnosability.
var (
	ErrNotWriteable       = errors.New("usp: attempt to update non-writeable parameter")
	ErrObjectDoesNotExist = errors.New("usp: object does not exist")
	ErrPermissionDenied   = errors.New("usp: permission denied")
	ErrCommandFailure     = errors.New("usp: command failure")
)

// sentinelForCode maps a code to its sentinel, for errors.Is.
var sentinelForCode = map[ErrorCode]error{
	ErrCodeNotWriteable:       ErrNotWriteable,
	ErrCodeObjectDoesNotExist: ErrObjectDoesNotExist,
	ErrCodePermissionDenied:   ErrPermissionDenied,
	ErrCodeCommandFailure:     ErrCommandFailure,
}

// ParamError is a per-parameter failure inside a USP error.
type ParamError struct {
	Path    string
	Code    ErrorCode
	Message string
}

// USPError is an error returned by an agent.
type USPError struct {
	Code        ErrorCode
	Message     string
	ParamErrors []ParamError
}

func (e *USPError) Error() string {
	if e == nil {
		return "usp: <nil>"
	}
	if e.Message == "" {
		return fmt.Sprintf("usp error %d (%s)", uint32(e.Code), e.Code)
	}
	return fmt.Sprintf("usp error %d (%s): %s", uint32(e.Code), e.Code, e.Message)
}

// Is lets errors.Is match a USPError against the sentinel for its code.
func (e *USPError) Is(target error) bool {
	if e == nil {
		return false
	}
	return sentinelForCode[e.Code] == target && target != nil
}

// ErrorFromMsg extracts a USPError from a message, or nil when the
// message is not an Error body.
//
// WARNING: the result is a typed *USPError, not the error interface. A
// nil *USPError assigned to an error interface variable produces a
// non-nil interface value (the classic Go typed-nil trap), so a caller
// that does `var err error = ErrorFromMsg(msg)` and then checks
// `err != nil` will take the error branch even for a normal response.
// Compare the *USPError result to nil before it is ever assigned to an
// error interface, e.g.:
//
//	if uspErr := ErrorFromMsg(msg); uspErr != nil {
//	    return uspErr
//	}
func ErrorFromMsg(msg *uspproto.Msg) *USPError {
	if msg == nil {
		return nil
	}
	body, ok := msg.GetBody().GetMsgBody().(*uspproto.Body_Error)
	if !ok || body.Error == nil {
		return nil
	}
	out := &USPError{
		Code:    ErrorCode(body.Error.GetErrCode()),
		Message: body.Error.GetErrMsg(),
	}
	for _, pe := range body.Error.GetParamErrs() {
		out.ParamErrors = append(out.ParamErrors, ParamError{
			Path:    pe.GetParamPath(),
			Code:    ErrorCode(pe.GetErrCode()),
			Message: pe.GetErrMsg(),
		})
	}
	return out
}
