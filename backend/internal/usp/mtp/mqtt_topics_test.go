package mtp

import "testing"

func TestReplyToFromV311Topic(t *testing.T) {
	const ctrl = "/usp/controller"
	cases := map[string]string{
		"/usp/controller/reply-to=%2Fusp%2Fagent":         "/usp/agent",
		"/usp/controller/reply-to=%2Fusp%2Fagent%2Fcpe-1": "/usp/agent/cpe-1",
		"/usp/controller/reply-to=agent-no-slashes":       "agent-no-slashes",
	}
	for in, want := range cases {
		got, ok := ReplyToFromV311Topic(in, ctrl)
		if !ok || got != want {
			t.Errorf("ReplyToFromV311Topic(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
}

func TestReplyToFromV311TopicRejectsForeign(t *testing.T) {
	const ctrl = "/usp/controller"
	for _, in := range []string{
		"/usp/controller",                    // no reply-to suffix at all
		"/usp/controller/other=thing",        // wrong key
		"/usp/other/reply-to=%2Fusp%2Fagent", // wrong controller topic
		"/usp/controller/reply-to=",          // empty agent topic
	} {
		if got, ok := ReplyToFromV311Topic(in, ctrl); ok {
			t.Errorf("ReplyToFromV311Topic(%q) = (%q, true), want ok=false", in, got)
		}
	}
}

func TestEscapeReplyToRoundTrip(t *testing.T) {
	for _, topic := range []string{"/usp/agent", "/usp/agent/cpe-1", "plain", "with%percent", "/a/b/c/d"} {
		esc := EscapeReplyTo(topic)
		if esc != topic && containsRune(esc, '/') {
			t.Errorf("EscapeReplyTo(%q) = %q still contains '/'", topic, esc)
		}
		got, ok := ReplyToFromV311Topic("/c/reply-to="+esc, "/c")
		if !ok || got != topic {
			t.Errorf("round trip of %q via %q gave (%q, %v)", topic, esc, got, ok)
		}
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
