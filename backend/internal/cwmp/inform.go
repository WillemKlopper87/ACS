package cwmp

// Inform is the RPC every CWMP session begins with.
type Inform struct {
	DeviceId      DeviceID               `xml:"DeviceId"`
	Event         []EventStruct          `xml:"Event>EventStruct"`
	MaxEnvelopes  int                    `xml:"MaxEnvelopes"`
	CurrentTime   string                 `xml:"CurrentTime"`
	RetryCount    int                    `xml:"RetryCount"`
	ParameterList []ParameterValueStruct `xml:"ParameterList>ParameterValueStruct"`
}

// EventCodes returns the raw event codes (e.g. "0 BOOTSTRAP", "2 PERIODIC")
// reported on this Inform.
func (i Inform) EventCodes() []string {
	codes := make([]string, 0, len(i.Event))
	for _, e := range i.Event {
		codes = append(codes, e.EventCode)
	}
	return codes
}

// HasEventCode reports whether the Inform carries the given numeric event
// code, e.g. HasEventCode("6") for CONNECTION REQUEST.
func (i Inform) HasEventCode(code string) bool {
	for _, e := range i.Event {
		if len(e.EventCode) > 0 && e.EventCode[0:1] == code {
			return true
		}
	}
	return false
}

// RenderInformResponse renders the InformResponse the ACS must send back
// immediately after receiving Inform. The id MUST be the same cwmp:ID
// value the CPE's Inform envelope carried — TR-069 §3.4.1.1 requires the
// response's ID header to match the request's, and many CPE stacks
// (Huawei, ZTE, MikroTik, Zyxel among others) validate this and abort or
// endlessly retry the session when it doesn't. Callers should fall back
// to a generated ID only when the Inform carried no ID header at all.
func RenderInformResponse(id string) []byte {
	return RenderInformResponseNS(id, DefaultCWMPNamespace)
}

// RenderInformResponseNS is RenderInformResponse with an explicit CWMP
// namespace URN, so the response speaks the same cwmp-1-x version as the
// CPE's Inform (see DetectCWMPNamespace).
func RenderInformResponseNS(id, ns string) []byte {
	return renderEnvelopeNS(id, `<cwmp:InformResponse><MaxEnvelopes>1</MaxEnvelopes></cwmp:InformResponse>`, ns)
}
