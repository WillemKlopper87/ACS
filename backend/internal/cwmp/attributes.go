package cwmp

import "strconv"

// SetParameterAttributes/GetParameterAttributes (TR-069 §A.3.2.3/§A.3.2.4)
// — nice-to-have backlog item: configures a parameter's active-notification
// behavior (0 = off, 1 = passive — reported on the next Inform anyway, 2 =
// active — the CPE Informs immediately on change) rather than the ACS
// having to poll for it. Every value write this codebase does today is
// ACS-initiated (SetParameterValues/GetParameterValues); this is the first
// RPC pair that changes what the *CPE* proactively tells the ACS.

// AttributeWrite is one parameter's requested notification level.
// AccessList is deliberately not exposed here — every write in this
// codebase already goes through operator RBAC at the REST layer, so
// per-parameter CPE-side access control (TR-069's own, separate
// mechanism) isn't a control surface this build needs to expose yet.
type AttributeWrite struct {
	Name         string
	Notification int // 0=off, 1=passive, 2=active
}

// RenderSetParameterAttributes renders a SetParameterAttributes request.
// AccessListChange is always sent as 0 (unchanged) since this build never
// writes AccessList.
func RenderSetParameterAttributes(id string, attrs []AttributeWrite) []byte {
	var sb []byte
	sb = append(sb, []byte(`<cwmp:SetParameterAttributes><ParameterList soap-enc:arrayType="cwmp:SetParameterAttributesStruct[`+strconv.Itoa(len(attrs))+`]">`)...)
	for _, a := range attrs {
		sb = append(sb, []byte(`<SetParameterAttributesStruct><Name>`+escapeXML(a.Name)+`</Name>`+
			`<NotificationChange>1</NotificationChange><Notification>`+strconv.Itoa(a.Notification)+`</Notification>`+
			`<AccessListChange>0</AccessListChange><AccessList soap-enc:arrayType="xsd:string[0]"></AccessList></SetParameterAttributesStruct>`)...)
	}
	sb = append(sb, []byte(`</ParameterList></cwmp:SetParameterAttributes>`)...)
	return renderEnvelope(id, string(sb))
}

// SetParameterAttributesResponse acknowledges the request — an empty
// element in the schema.
type SetParameterAttributesResponse struct{}

// RenderGetParameterAttributes renders a GetParameterAttributes request
// for the given parameter names.
func RenderGetParameterAttributes(id string, names []string) []byte {
	var sb []byte
	sb = append(sb, []byte(`<cwmp:GetParameterAttributes><ParameterNames soap-enc:arrayType="xsd:string[`+strconv.Itoa(len(names))+`]">`)...)
	for _, n := range names {
		sb = append(sb, []byte(`<string>`+escapeXML(n)+`</string>`)...)
	}
	sb = append(sb, []byte(`</ParameterNames></cwmp:GetParameterAttributes>`)...)
	return renderEnvelope(id, string(sb))
}

// ParameterAttributeStruct is one parameter's current attributes, as
// reported by GetParameterAttributesResponse.
type ParameterAttributeStruct struct {
	Name         string   `xml:"Name"`
	Notification int      `xml:"Notification"`
	AccessList   []string `xml:"AccessList>string"`
}

// GetParameterAttributesResponse carries the requested parameters'
// current attributes.
type GetParameterAttributesResponse struct {
	ParameterList []ParameterAttributeStruct `xml:"ParameterList>ParameterAttributeStruct"`
}
