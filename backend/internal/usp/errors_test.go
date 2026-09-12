package usp

import (
	"errors"
	"strings"
	"testing"

	"acs/internal/usp/uspproto"
)

func TestErrorCodeString(t *testing.T) {
	cases := map[ErrorCode]string{
		ErrCodeNotWriteable:       "attempt to update non-writeable parameter",
		ErrCodeObjectDoesNotExist: "object does not exist",
		ErrCodePermissionDenied:   "permission denied",
		ErrCodeCommandFailure:     "command failure",
		ErrCodeInvalidPath:        "invalid path",
		ErrCodeInvalidCommandArgs: "invalid command arguments",
	}
	for code, want := range cases {
		if got := code.String(); got != want {
			t.Errorf("ErrorCode(%d).String() = %q, want %q", code, got, want)
		}
	}
	// An unmapped code must still render usefully rather than blankly.
	if got := ErrorCode(7999).String(); !strings.Contains(got, "7999") {
		t.Errorf("unknown code rendered as %q, want it to mention 7999", got)
	}
	// The highest defined constant must render with its name, not fall
	// back to the bare number -- otherwise the table's upper bound has
	// silently drifted from errorCodeText.
	if got := ErrCodeInvalidCommandArgs.String(); got != "invalid command arguments" {
		t.Errorf("highest defined code rendered as %q, want its name", got)
	}
}

func TestErrorFromMsg(t *testing.T) {
	msg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: "m-1", MsgType: uspproto.Header_ERROR},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Error{Error: &uspproto.Error{
			ErrCode: uint32(ErrCodeNotWriteable),
			ErrMsg:  "KeyPassphrase is read-only",
			ParamErrs: []*uspproto.Error_ParamError{{
				ParamPath: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase",
				ErrCode:   uint32(ErrCodeNotWriteable),
				ErrMsg:    "read-only",
			}},
		}}},
	}
	uspErr := ErrorFromMsg(msg)
	if uspErr == nil {
		t.Fatal("ErrorFromMsg returned nil for an Error body")
	}
	if uspErr.Code != ErrCodeNotWriteable {
		t.Errorf("Code = %d, want %d", uspErr.Code, ErrCodeNotWriteable)
	}
	if uspErr.Message != "KeyPassphrase is read-only" {
		t.Errorf("Message = %q", uspErr.Message)
	}
	if len(uspErr.ParamErrors) != 1 || uspErr.ParamErrors[0].Path != "Device.WiFi.AccessPoint.1.Security.KeyPassphrase" {
		t.Errorf("ParamErrors = %+v", uspErr.ParamErrors)
	}
	// The error string must carry the code and message, since it is what
	// lands in a job's failure detail.
	if s := uspErr.Error(); !strings.Contains(s, "7013") || !strings.Contains(s, "read-only") {
		t.Errorf("Error() = %q, want it to mention 7013 and the message", s)
	}
}

// A nil *USPError must be safe to call methods on: a caller that returns
// ErrorFromMsg's result through an error interface gets a nil-receiver
// call the moment something does err.Error() or errors.Is(err, ...).
func TestUSPErrorNilReceiverSafe(t *testing.T) {
	var e *USPError
	if got := e.Error(); got != "usp: <nil>" {
		t.Errorf("(*USPError)(nil).Error() = %q, want %q", got, "usp: <nil>")
	}
	if e.Is(ErrNotWriteable) {
		t.Error("(*USPError)(nil).Is(ErrNotWriteable) = true, want false")
	}
}

// This is the trap itself, made harmless: assigning a nil *USPError to
// an error interface produces a non-nil interface value, so a caller
// must not rely on `err != nil` alone -- but calling Error() on it must
// not panic now that the receiver is nil-safe.
func TestErrorFromMsgTypedNilTrapIsHarmless(t *testing.T) {
	msg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: "m-nil", MsgType: uspproto.Header_GET_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetResp{GetResp: &uspproto.GetResp{}},
		}}},
	}
	var err error = ErrorFromMsg(msg)
	if err != nil {
		// This is the trap: err is a non-nil interface wrapping a nil
		// *USPError. It must not panic when exercised.
		_ = err.Error()
	}
}

func TestErrorFromMsgNilForNonError(t *testing.T) {
	msg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: "m-2", MsgType: uspproto.Header_GET_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetResp{GetResp: &uspproto.GetResp{}},
		}}},
	}
	if got := ErrorFromMsg(msg); got != nil {
		t.Errorf("ErrorFromMsg on a GetResp = %+v, want nil", got)
	}
	if got := ErrorFromMsg(nil); got != nil {
		t.Errorf("ErrorFromMsg(nil) = %+v, want nil", got)
	}
	// A Body_Error wrapper whose inner Error is nil is a malformed inbound
	// message. It must yield nil, not a nil-pointer dereference.
	hollow := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: "m-3", MsgType: uspproto.Header_ERROR},
		Body:   &uspproto.Body{MsgBody: &uspproto.Body_Error{Error: nil}},
	}
	if got := ErrorFromMsg(hollow); got != nil {
		t.Errorf("ErrorFromMsg on a Body_Error with nil Error = %+v, want nil", got)
	}
}

// The four codes later plans branch on must be matchable with errors.Is,
// so a dispatcher can say "this was a non-writeable parameter" without
// comparing integers at the call site.
func TestSentinelMatching(t *testing.T) {
	cases := []struct {
		code     ErrorCode
		sentinel error
	}{
		{ErrCodeNotWriteable, ErrNotWriteable},
		{ErrCodeObjectDoesNotExist, ErrObjectDoesNotExist},
		{ErrCodePermissionDenied, ErrPermissionDenied},
		{ErrCodeCommandFailure, ErrCommandFailure},
	}
	for _, c := range cases {
		err := error(&USPError{Code: c.code, Message: "x"})
		if !errors.Is(err, c.sentinel) {
			t.Errorf("USPError{Code: %d} does not match its sentinel", c.code)
		}
		// And must not match a different sentinel.
		for _, other := range cases {
			if other.code == c.code {
				continue
			}
			if errors.Is(err, other.sentinel) {
				t.Errorf("USPError{Code: %d} wrongly matches the sentinel for %d", c.code, other.code)
			}
		}
	}
}
