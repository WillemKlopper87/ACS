package cwmp

// SoapFault is a SOAP 1.1 Fault whose <detail> carries a CWMP FaultStruct.
// CWMP faults always arrive this way (never as a bare SOAP fault without
// the cwmp:Fault detail), so Detail is treated as required for parsing
// purposes even though the XML schema doesn't enforce it.
type SoapFault struct {
	FaultCode   string       `xml:"faultcode"`
	FaultString string       `xml:"faultstring"`
	Detail      *FaultDetail `xml:"detail"`
}

type FaultDetail struct {
	Fault *FaultStruct `xml:"Fault"`
}

// FaultStruct is the CWMP-specific fault payload (TR-069 Annex A fault
// codes: 9000-series).
type FaultStruct struct {
	FaultCode   string `xml:"FaultCode"`
	FaultString string `xml:"FaultString"`
}

// CWMPCode returns the CWMP fault code (e.g. "9005"), or "" if this SOAP
// fault has no CWMP detail (a malformed/non-conformant CPE response).
func (f *SoapFault) CWMPCode() string {
	if f == nil || f.Detail == nil || f.Detail.Fault == nil {
		return ""
	}
	return f.Detail.Fault.FaultCode
}

// CWMPMessage returns the CWMP fault string, or the bare SOAP faultstring
// if there is no CWMP detail.
func (f *SoapFault) CWMPMessage() string {
	if f == nil {
		return ""
	}
	if f.Detail != nil && f.Detail.Fault != nil && f.Detail.Fault.FaultString != "" {
		return f.Detail.Fault.FaultString
	}
	return f.FaultString
}
