// Package stun implements just enough of RFC 5389 (Session Traversal
// Utilities for NAT) to answer Binding Requests: a UDP listener that tells
// a CPE what address/port it appears as from the outside, the first half
// of TR-069 Annex G's NAT-traversal mechanism (build plan critical backlog
// item #1). The CPE's STUN client is expected to point at this server (its
// own ManagementServer.STUNServerAddress/Port), keeping a NAT binding open
// with periodic re-binds; the CPE then reports what it learned back to the
// ACS via the ordinary Inform parameters
// (Device.ManagementServer.UDPConnectionRequestAddress/NATDetected — see
// cmd/acs's Inform handling), not through this server directly.
//
// Deliberately not implemented: message integrity (STUN USERNAME/
// MESSAGE-INTEGRITY, RFC 5389 §10) — this server never sends a 401
// challenge requesting it, so a CPE configured with STUNUsername/
// STUNPassword simply won't send them, which is within spec ("only if
// message integrity has been requested by the STUN server"). Also not
// implemented: the second half of Annex G, the ACS-to-CPE UDP Connection
// Request datagram itself — its exact signature format could not be
// sourced from the authoritative spec (paywalled/blocked in every attempt);
// shipping a guessed HMAC scheme would look done while silently failing
// against real hardware, so that piece is deferred until it can be
// determined from a packet capture against a real device instead of a
// guess.
package stun

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
)

const (
	magicCookie = 0x2112A442

	classRequest  = 0x0000
	classResponse = 0x0100 // set on the response's method's high bits (type | 0x0100)
	methodBinding = 0x0001

	typeBindingRequest  = classRequest | methodBinding  // 0x0001
	typeBindingResponse = classResponse | methodBinding // 0x0101

	attrMappedAddress    = 0x0001
	attrXorMappedAddress = 0x0020

	familyIPv4 = 0x01
	familyIPv6 = 0x02

	headerLen = 20
)

// Server is a minimal RFC 5389 STUN server: one UDP socket, answers every
// well-formed Binding Request, ignores everything else. No state is kept
// between requests — a STUN Binding Request/Response exchange is
// stateless by design (RFC 5389 §1).
type Server struct {
	logger *slog.Logger
	conn   *net.UDPConn
}

// Listen opens the UDP socket. addr is typically ":3478" (RFC 5389's
// assigned default port).
func Listen(addr string, logger *slog.Logger) (*Server, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve stun listen addr: %w", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp for stun: %w", err)
	}
	return &Server{logger: logger, conn: conn}, nil
}

// Run answers Binding Requests until ctx is canceled.
func (s *Server) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = s.conn.Close()
	}()

	buf := make([]byte, 1500) // STUN messages are small; UDP MTU is a generous upper bound
	for {
		n, remote, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return // Close() from the goroutine above, not a real error
			}
			s.logger.Warn("stun read error", "err", err)
			continue
		}

		resp, ok := handleBindingRequest(buf[:n], remote)
		if !ok {
			continue // not a Binding Request we recognize — silently ignore, matches how most STUN servers treat noise/other methods
		}
		if _, err := s.conn.WriteToUDP(resp, remote); err != nil {
			s.logger.Warn("stun write error", "err", err, "remote", remote)
		}
	}
}

func (s *Server) Close() error {
	return s.conn.Close()
}

// handleBindingRequest parses a STUN message and, if it's a well-formed
// Binding Request, builds a Binding Success Response carrying the remote's
// reflexive address as both XOR-MAPPED-ADDRESS (RFC 5389, what modern
// clients read) and MAPPED-ADDRESS (RFC 3489, for older/classic STUN
// clients that ignore the XOR variant) — sending both costs nothing and
// maximizes the odds of interoperating with whatever the CPE actually
// implements.
func handleBindingRequest(msg []byte, remote *net.UDPAddr) ([]byte, bool) {
	if len(msg) < headerLen {
		return nil, false
	}
	msgType := binary.BigEndian.Uint16(msg[0:2])
	msgLen := binary.BigEndian.Uint16(msg[2:4])
	cookie := binary.BigEndian.Uint32(msg[4:8])
	transactionID := msg[8:20]

	if msgType != typeBindingRequest || cookie != magicCookie {
		return nil, false
	}
	if int(msgLen)+headerLen > len(msg) {
		return nil, false // declared body longer than what actually arrived
	}

	ip4 := remote.IP.To4()
	if ip4 == nil {
		return nil, false // IPv6 reflexive addresses aren't needed here — every CGNAT case this project targets is IPv4
	}

	var body []byte
	body = appendMappedAddress(body, attrXorMappedAddress, ip4, uint16(remote.Port), transactionID)
	body = appendMappedAddress(body, attrMappedAddress, ip4, uint16(remote.Port), transactionID)

	resp := make([]byte, headerLen+len(body))
	binary.BigEndian.PutUint16(resp[0:2], typeBindingResponse)
	binary.BigEndian.PutUint16(resp[2:4], uint16(len(body)))
	binary.BigEndian.PutUint32(resp[4:8], magicCookie)
	copy(resp[8:20], transactionID)
	copy(resp[20:], body)
	return resp, true
}

// appendMappedAddress appends one MAPPED-ADDRESS or XOR-MAPPED-ADDRESS
// attribute (RFC 5389 §15.1/§15.2) for an IPv4 reflexive address. For the
// XOR variant, the port is XORed with the top 16 bits of the magic cookie
// and the address is XORed with the magic cookie's 4 bytes, both per
// §15.2 — for the plain variant neither is XORed.
func appendMappedAddress(dst []byte, attrType uint16, ip4 net.IP, port uint16, transactionID []byte) []byte {
	value := make([]byte, 8)
	value[0] = 0 // reserved
	value[1] = familyIPv4

	xor := attrType == attrXorMappedAddress

	p := port
	if xor {
		p ^= uint16(magicCookie >> 16)
	}
	binary.BigEndian.PutUint16(value[2:4], p)

	addr := make([]byte, 4)
	copy(addr, ip4)
	if xor {
		var cookieBytes [4]byte
		binary.BigEndian.PutUint32(cookieBytes[:], magicCookie)
		for i := range addr {
			addr[i] ^= cookieBytes[i]
		}
	}
	copy(value[4:8], addr)

	dst = binary.BigEndian.AppendUint16(dst, attrType)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(value)))
	dst = append(dst, value...)
	return dst
}
