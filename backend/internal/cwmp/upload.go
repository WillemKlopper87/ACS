package cwmp

import "strconv"

// Upload (TR-069 §A.3.2.7) — nice-to-have backlog item, Download's mirror
// image: the CPE pushes a file *to* the ACS (a vendor config backup, a log
// file) rather than fetching one. Same CPE-initiates-the-transfer shape as
// Download (v3 §19.4's "never send bytes inside a SOAP call" applies here
// too — Upload only ever carries a URL for the CPE to PUT/POST to), and
// the same asynchronous completion signal: TransferComplete, the exact
// RPC Download already uses, not a separate "UploadComplete".
func RenderUpload(id, commandKey, fileType, url, username, password string, delaySeconds int) []byte {
	body := `<cwmp:Upload>` +
		`<CommandKey>` + escapeXML(commandKey) + `</CommandKey>` +
		`<FileType>` + escapeXML(fileType) + `</FileType>` +
		`<URL>` + escapeXML(url) + `</URL>` +
		`<Username>` + escapeXML(username) + `</Username>` +
		`<Password>` + escapeXML(password) + `</Password>` +
		`<DelaySeconds>` + strconv.Itoa(delaySeconds) + `</DelaySeconds>` +
		`</cwmp:Upload>`
	return renderEnvelope(id, body)
}

// UploadResponse is the CPE's synchronous acknowledgement — Status 1
// means "accepted, the actual HTTP PUT/POST to URL and the eventual
// TransferComplete both happen asynchronously," the overwhelmingly common
// case and the only one this build distinguishes (same simplification
// Download already makes for its Status 0 "completed synchronously"
// case).
type UploadResponse struct {
	Status       int    `xml:"Status"`
	StartTime    string `xml:"StartTime"`
	CompleteTime string `xml:"CompleteTime"`
}
