package cwmp

import (
	"bytes"
	"encoding/xml"
	"regexp"
)

// InboundEnvelope covers every message shape the ACS needs to receive in
// Phase 0/1: Inform, the responses to the probe RPCs we send, and Fault.
// encoding/xml matches struct-tagged element names by local name only when
// no namespace is given in the tag, so this parses regardless of which
// SOAP prefix (soap-env, soapenv, SOAP-ENV, ...) or CWMP namespace version
// (cwmp-1-0 .. cwmp-1-4) a given CPE happens to use.
type InboundEnvelope struct {
	XMLName xml.Name      `xml:"Envelope"`
	Header  InboundHeader `xml:"Header"`
	Body    InboundBody   `xml:"Body"`
}

type InboundHeader struct {
	ID string `xml:"ID"`
}

type InboundBody struct {
	Inform                         *Inform                         `xml:"Inform"`
	GetRPCMethodsResponse          *GetRPCMethodsResponse          `xml:"GetRPCMethodsResponse"`
	GetParameterNamesResponse      *GetParameterNamesResponse      `xml:"GetParameterNamesResponse"`
	GetParameterValuesResponse     *GetParameterValuesResponse     `xml:"GetParameterValuesResponse"`
	SetParameterValuesResponse     *SetParameterValuesResponse     `xml:"SetParameterValuesResponse"`
	DownloadResponse               *DownloadResponse               `xml:"DownloadResponse"`
	TransferComplete               *TransferComplete               `xml:"TransferComplete"`
	AddObjectResponse              *AddObjectResponse              `xml:"AddObjectResponse"`
	DeleteObjectResponse           *DeleteObjectResponse           `xml:"DeleteObjectResponse"`
	RebootResponse                 *RebootResponse                 `xml:"RebootResponse"`
	FactoryResetResponse           *FactoryResetResponse           `xml:"FactoryResetResponse"`
	ScheduleInformResponse         *ScheduleInformResponse         `xml:"ScheduleInformResponse"`
	SetParameterAttributesResponse *SetParameterAttributesResponse `xml:"SetParameterAttributesResponse"`
	GetParameterAttributesResponse *GetParameterAttributesResponse `xml:"GetParameterAttributesResponse"`
	UploadResponse                 *UploadResponse                 `xml:"UploadResponse"`
	Fault                          *SoapFault                      `xml:"Fault"`
}

// IsEmpty reports whether this is the empty-body POST a CPE sends to ask
// "do you have any RPCs for me?" after InformResponse.
func (b InboundBody) IsEmpty() bool {
	return b.Inform == nil &&
		b.GetRPCMethodsResponse == nil &&
		b.GetParameterNamesResponse == nil &&
		b.GetParameterValuesResponse == nil &&
		b.SetParameterValuesResponse == nil &&
		b.DownloadResponse == nil &&
		b.TransferComplete == nil &&
		b.AddObjectResponse == nil &&
		b.DeleteObjectResponse == nil &&
		b.RebootResponse == nil &&
		b.FactoryResetResponse == nil &&
		b.ScheduleInformResponse == nil &&
		b.SetParameterAttributesResponse == nil &&
		b.GetParameterAttributesResponse == nil &&
		b.UploadResponse == nil &&
		b.Fault == nil
}

// ParseEnvelope parses a raw CWMP HTTP request body. An empty body (the
// CPE's "no more to say, do you have work for me" POST) is valid and
// returns a zero-value envelope with no error. Whitespace-only bodies
// count as empty too — CPEs in the wild send a bare CRLF or a few spaces
// as their empty POST, and rejecting those as malformed XML breaks the
// session right after InformResponse.
func ParseEnvelope(raw []byte) (*InboundEnvelope, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return &InboundEnvelope{}, nil
	}
	var env InboundEnvelope
	if err := xml.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

var cwmpNamespaceRE = regexp.MustCompile(`urn:dslforum-org:cwmp-1-\d+`)

// DetectCWMPNamespace returns the CWMP namespace URN the CPE used in its
// envelope (urn:dslforum-org:cwmp-1-0 through cwmp-1-4). Responses to
// CPE-initiated RPCs must be rendered in the same namespace version the
// CPE spoke — strict CPE stacks fault on a version mismatch. Falls back
// to DefaultCWMPNamespace when the body carries no CWMP namespace (e.g.
// an empty poll POST).
func DetectCWMPNamespace(raw []byte) string {
	if m := cwmpNamespaceRE.Find(raw); m != nil {
		return string(m)
	}
	return DefaultCWMPNamespace
}
