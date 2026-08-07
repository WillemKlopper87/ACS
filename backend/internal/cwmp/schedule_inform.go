package cwmp

import "strconv"

// ScheduleInform (TR-069 §A.3.2.10) — nice-to-have backlog item: tells a
// CPE to Inform again after a delay, rather than waiting for its own
// periodic interval or an ACS-initiated Connection Request. Useful for
// "check back with me in an hour" without needing the CPE to be
// Connection-Request-reachable at all.
func RenderScheduleInform(id, commandKey string, delaySeconds int) []byte {
	body := `<cwmp:ScheduleInform>` +
		`<DelaySeconds>` + strconv.Itoa(delaySeconds) + `</DelaySeconds>` +
		`<CommandKey>` + escapeXML(commandKey) + `</CommandKey>` +
		`</cwmp:ScheduleInform>`
	return renderEnvelope(id, body)
}

// ScheduleInformResponse is the CPE's synchronous acknowledgement — an
// empty element in the schema, same shape as Reboot/FactoryReset.
type ScheduleInformResponse struct{}
