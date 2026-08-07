package cwmp

import "testing"

func TestProbeSessionSerialDispatch(t *testing.T) {
	store := NewSessionStore()
	deviceID := DeviceID{
		Manufacturer: "Zyxel",
		OUI:          "001349",
		ProductClass: "NR5103",
		SerialNumber: "S230Q12345678",
	}

	session := store.StartOrResume(deviceID, []string{"1 BOOT"})

	// Step 1: GetRPCMethods
	body1, step, ok := session.NextRequest("id-1")
	if !ok || step != StepGetRPCMethods {
		t.Fatalf("step 1 = (%v, %v), want (%v, true)", step, ok, StepGetRPCMethods)
	}
	// The v3-corrected model requires one in-flight RPC at a time: calling
	// NextRequest again before CompleteCurrent must not advance the queue —
	// it must hand back the same in-flight request (design doc v3 §5.4 /
	// §19.1: "Do not fire RPCs in parallel inside one CWMP session").
	body2, step2, ok2 := session.NextRequest("id-1-retry")
	if !ok2 || step2 != step || string(body2) != string(body1) {
		t.Fatalf("NextRequest called again before CompleteCurrent advanced the queue: got step %v, want the same in-flight step %v", step2, step)
	}

	session.CompleteCurrent(InboundBody{
		GetRPCMethodsResponse: &GetRPCMethodsResponse{MethodList: []string{"Inform", "Download"}},
	})

	// Step 2: GetParameterNames(Device.)
	_, stepD2, okD2 := session.NextRequest("id-2")
	if !okD2 || stepD2 != StepGetParameterNamesD2 {
		t.Fatalf("step 2 = (%v, %v), want (%v, true)", stepD2, okD2, StepGetParameterNamesD2)
	}
	session.CompleteCurrent(InboundBody{
		GetParameterNamesResponse: &GetParameterNamesResponse{
			ParameterList: []ParameterInfoStruct{{Name: "Device.DeviceInfo.SoftwareVersion", Writable: "0"}},
		},
	})

	// Step 3: GetParameterNames(InternetGatewayDevice.)
	_, step3, ok3 := session.NextRequest("id-3")
	if !ok3 || step3 != StepGetParameterNamesIGD {
		t.Fatalf("step 3 = (%v, %v), want (%v, true)", step3, ok3, StepGetParameterNamesIGD)
	}
	session.CompleteCurrent(InboundBody{
		Fault: &SoapFault{Detail: &FaultDetail{Fault: &FaultStruct{FaultCode: "9000", FaultString: "not supported"}}},
	})

	// Sequence exhausted.
	_, _, ok4 := session.NextRequest("id-4")
	if ok4 {
		t.Fatal("expected probe sequence to be exhausted, but NextRequest returned ok=true")
	}

	_, _, results := session.Snapshot()
	if len(results.RPCMethods) != 2 {
		t.Errorf("RPCMethods = %v, want 2 entries", results.RPCMethods)
	}
	if !results.Device2Supported || results.Device2ParamCount != 1 {
		t.Errorf("Device2Supported=%v Device2ParamCount=%d, want true/1", results.Device2Supported, results.Device2ParamCount)
	}
	if results.IGD1Supported {
		t.Error("IGD1Supported = true, want false (fault was recorded instead of success)")
	}
	if results.Faults[StepGetParameterNamesIGD] == "" {
		t.Error("expected a recorded fault for StepGetParameterNamesIGD")
	}
}

func TestSessionStoreStartOrResume(t *testing.T) {
	store := NewSessionStore()
	deviceID := DeviceID{OUI: "001349", SerialNumber: "SN1"}

	first := store.StartOrResume(deviceID, []string{"0 BOOTSTRAP"})
	second := store.StartOrResume(deviceID, []string{"2 PERIODIC"})

	if first != second {
		t.Error("expected StartOrResume to return the same in-flight session for the same device")
	}
	if second.EventCodes[0] != "2 PERIODIC" {
		t.Errorf("expected event codes to be refreshed on resume, got %v", second.EventCodes)
	}

	got, ok := store.Get(deviceID.NaturalKey())
	if !ok || got != first {
		t.Error("Get() did not return the session created by StartOrResume")
	}
}
