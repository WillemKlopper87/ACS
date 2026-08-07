package cwmp

// Reboot and FactoryReset (TR-069 §A.3.2.8/§A.3.2.9) — table-stakes
// support-agent actions ("power-cycle this device," "wipe it back to
// factory defaults before an RMA") that were missing from this codebase's
// protocol coverage entirely. Both are simple, argument-light RPCs: the
// CPE drops the TCP connection and restarts, so there is no
// RebootResponse/FactoryResetResponse-carried result to speak of beyond
// "accepted" — the real outcome shows up as a fresh Inform with event
// code "M Reboot" (or "0 BOOTSTRAP" after a factory reset re-provisions
// from scratch), the same "don't trust the ack, watch what happens next"
// pattern Download/TransferComplete already established for firmware.

// RenderReboot renders a Reboot request. commandKey is echoed back on the
// CPE's next Inform (TR-069 §3.7.1.5's "M Reboot" event correlates to
// whichever CommandKey caused it), the same correlation convention every
// other ACS-initiated RPC in this codebase uses.
func RenderReboot(id, commandKey string) []byte {
	body := `<cwmp:Reboot><CommandKey>` + escapeXML(commandKey) + `</CommandKey></cwmp:Reboot>`
	return renderEnvelope(id, body)
}

// RebootResponse is the CPE's synchronous acknowledgement that it will
// reboot — an empty element in the schema; presence alone is the signal.
type RebootResponse struct{}

// RenderFactoryReset renders a FactoryReset request — no arguments at
// all; TR-069 gives the CPE no say in what "factory defaults" means.
func RenderFactoryReset(id string) []byte {
	return renderEnvelope(id, `<cwmp:FactoryReset></cwmp:FactoryReset>`)
}

// FactoryResetResponse is the CPE's synchronous acknowledgement.
type FactoryResetResponse struct{}
