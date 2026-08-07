package cwmp

// AddObject and DeleteObject (TR-069 §A.3.2.5/§A.3.2.6) — the RPC pair
// this codebase's protocol coverage lacked entirely until now: every
// prior write path (SetParameterValues) can only edit parameters that
// already exist on the device. Provisioning a *new* table row — a second
// WLAN SSID, a port-forward rule, a VoIP line — requires AddObject first,
// which returns the new row's instance number; only then can
// SetParameterValues target it (e.g. "Device.WiFi.SSID.3.SSID" once
// AddObject on "Device.WiFi.SSID." returned InstanceNumber=3).

// RenderAddObject renders an AddObject request. objectPath is the
// parent's path ending in "." (e.g. "Device.WiFi.SSID.") — the CPE picks
// the new instance number itself and returns it in AddObjectResponse.
// parameterKey is echoed back on the "4 VALUE CHANGE"/object-creation
// Inform event, the same correlation mechanism SetParameterValues uses.
func RenderAddObject(id, objectPath, parameterKey string) []byte {
	body := `<cwmp:AddObject>` +
		`<ObjectName>` + escapeXML(objectPath) + `</ObjectName>` +
		`<ParameterKey>` + escapeXML(parameterKey) + `</ParameterKey>` +
		`</cwmp:AddObject>`
	return renderEnvelope(id, body)
}

// AddObjectResponse carries the newly created row's instance number.
// Status 1 means "created, but a further action (often a subsequent
// SetParameterValues + apply) is needed before it takes effect" — the
// same "Status 1 = accepted-but-pending" shape SetParameterValuesResponse
// already established, not treated differently here.
type AddObjectResponse struct {
	InstanceNumber int `xml:"InstanceNumber"`
	Status         int `xml:"Status"`
}

// RenderDeleteObject renders a DeleteObject request. objectPath is the
// full path to the specific instance being removed, ending in "." (e.g.
// "Device.WiFi.SSID.3.").
func RenderDeleteObject(id, objectPath, parameterKey string) []byte {
	body := `<cwmp:DeleteObject>` +
		`<ObjectName>` + escapeXML(objectPath) + `</ObjectName>` +
		`<ParameterKey>` + escapeXML(parameterKey) + `</ParameterKey>` +
		`</cwmp:DeleteObject>`
	return renderEnvelope(id, body)
}

// DeleteObjectResponse acknowledges a DeleteObject request.
type DeleteObjectResponse struct {
	Status int `xml:"Status"`
}
