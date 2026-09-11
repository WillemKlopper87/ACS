package mtp

import "strings"

// ContentTypeUSP is the MQTT 5 Content Type property value the USP MQTT
// binding requires on every PUBLISH carrying a USP Record, so a peer
// can identify the payload without inspecting it.
const ContentTypeUSP = "usp.msg"

// replyToKey is the topic-level key an MQTT 3.1.1 publisher appends to
// the topic it publishes on, so the party receiving that publish knows
// where to send its reply. There is no MQTT 5 Response Topic property
// in 3.1.1, so the USP MQTT binding carries the same information as a
// topic suffix instead.
const replyToKey = "/reply-to="

// ReplyToFromV311Topic extracts the reply-to topic that an MQTT 3.1.1
// publisher embedded in publishedTopic, given the topic this side
// subscribes on (controllerTopic). It returns ok=false when
// publishedTopic does not carry a well-formed "<controllerTopic>/reply-to=<escaped topic>"
// suffix for controllerTopic specifically -- including a foreign
// controller topic, a different suffix key, or an empty escaped topic
// -- since none of those identify a usable reply destination.
//
// The returned agentTopic has had "%2F" unescaped back to "/", the
// inverse of EscapeReplyTo.
func ReplyToFromV311Topic(publishedTopic, controllerTopic string) (agentTopic string, ok bool) {
	prefix := controllerTopic + replyToKey
	if !strings.HasPrefix(publishedTopic, prefix) {
		return "", false
	}
	escaped := publishedTopic[len(prefix):]
	if escaped == "" {
		return "", false
	}
	return strings.ReplaceAll(escaped, "%2F", "/"), true
}

// EscapeReplyTo encodes agentTopic for embedding as the "reply-to="
// suffix of an MQTT 3.1.1 publish topic: "/" -- otherwise a topic-level
// separator -- becomes "%2F" so the whole reply-to topic survives as a
// single topic level. It is the inverse of the unescaping
// ReplyToFromV311Topic performs.
func EscapeReplyTo(agentTopic string) string {
	return strings.ReplaceAll(agentTopic, "/", "%2F")
}
