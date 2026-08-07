package cwmp

import (
	"strconv"
	"strings"
)

// This file implements the two Phase 0 "capability probe" RPCs (v3 §14
// Phase 0 / build plan §4 Phase 0): GetRPCMethods and GetParameterNames.
// These are what let the lab harness discover, per real device, which
// RPCs it supports and whether it roots its parameter tree at
// "Device." (TR-181/Device:2) or "InternetGatewayDevice." (TR-098/IGD:1) —
// resolving prerequisite P1/P2 from the design doc.

// GetRPCMethodsResponse lists the RPC methods a CPE supports.
type GetRPCMethodsResponse struct {
	MethodList []string `xml:"MethodList>string"`
}

// RenderGetRPCMethods renders a GetRPCMethods request.
func RenderGetRPCMethods(id string) []byte {
	return renderEnvelope(id, `<cwmp:GetRPCMethods></cwmp:GetRPCMethods>`)
}

// GetParameterNamesResponse lists parameter names/writability under the
// requested path.
type GetParameterNamesResponse struct {
	ParameterList []ParameterInfoStruct `xml:"ParameterList>ParameterInfoStruct"`
}

// RenderGetParameterNames renders a GetParameterNames request for the
// given root path (e.g. "Device." or "InternetGatewayDevice.").
// nextLevel=false requests the full subtree in one call, which is what
// the Phase 0 probe wants (a complete parameter inventory).
func RenderGetParameterNames(id, path string, nextLevel bool) []byte {
	nl := "0"
	if nextLevel {
		nl = "1"
	}
	body := `<cwmp:GetParameterNames><ParameterPath>` + escapeXML(path) +
		`</ParameterPath><NextLevel>` + nl + `</NextLevel></cwmp:GetParameterNames>`
	return renderEnvelope(id, body)
}

// GetParameterValuesResponse carries the parameter values requested by a
// GetParameterValues RPC.
type GetParameterValuesResponse struct {
	ParameterList []ParameterValueStruct `xml:"ParameterList>ParameterValueStruct"`
}

// RenderGetParameterValues renders a GetParameterValues request for the
// given parameter paths (build plan §4 Phase 2).
func RenderGetParameterValues(id string, names []string) []byte {
	var sb strings.Builder
	sb.WriteString(`<cwmp:GetParameterValues><ParameterNames soap-enc:arrayType="xsd:string[`)
	sb.WriteString(strconv.Itoa(len(names)))
	sb.WriteString(`]">`)
	for _, n := range names {
		sb.WriteString(`<string>`)
		sb.WriteString(escapeXML(n))
		sb.WriteString(`</string>`)
	}
	sb.WriteString(`</ParameterNames></cwmp:GetParameterValues>`)
	return renderEnvelope(id, sb.String())
}

// SetParameterValuesResponse is the CPE's acknowledgement of a
// SetParameterValues RPC. Status 0 means the values were applied without
// requiring a reboot; Status 1 means they were applied but a reboot is
// needed before they take effect (TR-069 §A.3.2.2) — Phase 2 records
// either as success, since applying a required-reboot job type is a later
// concern (build plan §4 Phase 2 scope is basic provisioning, not
// reboot-orchestration).
type SetParameterValuesResponse struct {
	Status int `xml:"Status"`
}

// RenderSetParameterValues renders a SetParameterValues request.
// parameterKey is echoed back by the CPE on the Inform that reports the
// resulting "4 VALUE CHANGE" event, letting a later phase correlate that
// event to the job that caused it — Phase 2 sets it (to the job's
// CommandKey) but does not yet consume it on the Inform side.
func RenderSetParameterValues(id string, params []ParameterValueStruct, parameterKey string) []byte {
	var sb strings.Builder
	sb.WriteString(`<cwmp:SetParameterValues><ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[`)
	sb.WriteString(strconv.Itoa(len(params)))
	sb.WriteString(`]">`)
	for _, p := range params {
		sb.WriteString(`<ParameterValueStruct><Name>`)
		sb.WriteString(escapeXML(p.Name))
		sb.WriteString(`</Name><Value xsi:type="xsd:string">`)
		sb.WriteString(escapeXML(p.Value))
		sb.WriteString(`</Value></ParameterValueStruct>`)
	}
	sb.WriteString(`</ParameterList><ParameterKey>`)
	sb.WriteString(escapeXML(parameterKey))
	sb.WriteString(`</ParameterKey></cwmp:SetParameterValues>`)
	return renderEnvelope(id, sb.String())
}

// TransferComplete reports the outcome of a Download (build plan §4
// Phase 4). Per TR-069, FaultStruct is present on *every* TransferComplete,
// success included — FaultCode "0" means no fault, not "no FaultStruct
// element." Checking FaultStruct != nil alone would misread every
// successful transfer as a failure; use IsFault instead.
type TransferComplete struct {
	CommandKey  string       `xml:"CommandKey"`
	FaultStruct *FaultStruct `xml:"FaultStruct"`
}

// IsFault reports whether this TransferComplete indicates a failure.
func (tc *TransferComplete) IsFault() bool {
	return tc.FaultStruct != nil && tc.FaultStruct.FaultCode != "" && tc.FaultStruct.FaultCode != "0"
}

func escapeXML(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			out = append(out, []byte("&amp;")...)
		case '<':
			out = append(out, []byte("&lt;")...)
		case '>':
			out = append(out, []byte("&gt;")...)
		case '"':
			out = append(out, []byte("&quot;")...)
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
