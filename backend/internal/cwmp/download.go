package cwmp

import "strconv"

// This file implements the Firmware OTA RPCs (design doc v3 §9, build
// plan §4 Phase 4): Download and TransferComplete. Never send firmware
// bytes inside a SOAP parameter write (v3 §19.4) — Download only ever
// carries a URL; the CPE fetches the image itself (CPE-pull, not
// ACS-push).

// RenderDownload renders a Download request. commandKey correlates the
// eventual TransferComplete back to the job that issued this — the CPE
// may not send TransferComplete until a *later* session (after it
// fetches, flashes, and reboots), so CommandKey is the only thing tying
// the two together; there is no session-level correlation to rely on
// (v3 §9.2/§19.4).
func RenderDownload(id, commandKey, fileType, url, username, password string, fileSize int64, targetFilename string, delaySeconds int) []byte {
	body := `<cwmp:Download>` +
		`<CommandKey>` + escapeXML(commandKey) + `</CommandKey>` +
		`<FileType>` + escapeXML(fileType) + `</FileType>` +
		`<URL>` + escapeXML(url) + `</URL>` +
		`<Username>` + escapeXML(username) + `</Username>` +
		`<Password>` + escapeXML(password) + `</Password>` +
		`<FileSize>` + strconv.FormatInt(fileSize, 10) + `</FileSize>` +
		`<TargetFileName>` + escapeXML(targetFilename) + `</TargetFileName>` +
		`<DelaySeconds>` + strconv.Itoa(delaySeconds) + `</DelaySeconds>` +
		`<SuccessURL></SuccessURL>` +
		`<FailureURL></FailureURL>` +
		`</cwmp:Download>`
	return renderEnvelope(id, body)
}

// DownloadResponse is the CPE's synchronous acknowledgement of a Download
// request. Status 1 means "accepted, transfer will happen asynchronously
// and TransferComplete will follow" — the overwhelmingly common case, and
// the reason a job must not be marked SUCCESS here (v3 §9.2: "does not
// mean the firmware transfer has completed", §19.7). Status 0 (transfer
// already completed synchronously) is rare enough in practice that this
// build treats every DownloadResponse the same way — move to
// AWAITING_TRANSFER_COMPLETE and wait for the real signal — rather than
// special-casing a path with no live device to verify it against.
type DownloadResponse struct {
	Status       int    `xml:"Status"`
	StartTime    string `xml:"StartTime"`
	CompleteTime string `xml:"CompleteTime"`
}

// RenderTransferCompleteResponse is the empty acknowledgement the ACS
// sends back after receiving TransferComplete. As with InformResponse,
// the id must echo the cwmp:ID the CPE's TransferComplete carried
// (TR-069 §3.4.1.1).
func RenderTransferCompleteResponse(id string) []byte {
	return RenderTransferCompleteResponseNS(id, DefaultCWMPNamespace)
}

// RenderTransferCompleteResponseNS is RenderTransferCompleteResponse with
// an explicit CWMP namespace URN (see DetectCWMPNamespace).
func RenderTransferCompleteResponseNS(id, ns string) []byte {
	return renderEnvelopeNS(id, `<cwmp:TransferCompleteResponse></cwmp:TransferCompleteResponse>`, ns)
}
