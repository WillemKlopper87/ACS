-- Critical feature backlog: STUN NAT traversal. Captures the two Inform
-- parameters a STUN-enabled CPE reports once it's bound against the ACS's
-- STUN server (internal/stun) — udp_connection_request_address is the
-- reflexive host:port the CPE learned from its STUN Binding Response;
-- nat_detected is whether the CPE itself concluded it's behind a NAT/port
-- mapping. Recording these is deliberately separate from sending the
-- Annex G UDP Connection Request datagram itself (not implemented yet —
-- see [[project-acs-stun-annex-g-wire-format]] in the build plan): even
-- without that, having these two fields populated tells us whether a real
-- device (e.g. a CGNAT'd CPE under test) is STUN-capable at all.
ALTER TABLE devices ADD COLUMN udp_connection_request_address TEXT;
ALTER TABLE devices ADD COLUMN nat_detected BOOLEAN;
