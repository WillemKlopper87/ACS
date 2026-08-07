package stun

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// buildBindingRequest constructs a minimal, valid RFC 5389 Binding Request
// with a random transaction ID, mirroring what a real STUN client sends.
func buildBindingRequest(t *testing.T) (msg []byte, transactionID []byte) {
	t.Helper()
	transactionID = make([]byte, 12)
	if _, err := rand.Read(transactionID); err != nil {
		t.Fatalf("generate transaction id: %v", err)
	}
	msg = make([]byte, headerLen)
	binary.BigEndian.PutUint16(msg[0:2], typeBindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], transactionID)
	return msg, transactionID
}

// parseXorMappedAddress walks a Binding Response's attributes and decodes
// XOR-MAPPED-ADDRESS the same way a real STUN client would, independently
// of appendMappedAddress — so this test can't pass just because encode and
// decode share a bug.
func parseXorMappedAddress(t *testing.T, resp []byte) (net.IP, uint16) {
	t.Helper()
	if len(resp) < headerLen {
		t.Fatalf("response too short: %d bytes", len(resp))
	}
	msgType := binary.BigEndian.Uint16(resp[0:2])
	if msgType != typeBindingResponse {
		t.Fatalf("response type = 0x%04x, want 0x%04x (Binding Success Response)", msgType, typeBindingResponse)
	}
	bodyLen := binary.BigEndian.Uint16(resp[2:4])
	body := resp[headerLen : headerLen+int(bodyLen)]

	for len(body) >= 4 {
		attrType := binary.BigEndian.Uint16(body[0:2])
		attrLen := binary.BigEndian.Uint16(body[2:4])
		value := body[4 : 4+int(attrLen)]

		if attrType == attrXorMappedAddress {
			family := value[1]
			if family != familyIPv4 {
				t.Fatalf("unexpected address family: %d", family)
			}
			port := binary.BigEndian.Uint16(value[2:4]) ^ uint16(magicCookie>>16)

			var cookieBytes [4]byte
			binary.BigEndian.PutUint32(cookieBytes[:], magicCookie)
			ip := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				ip[i] = value[4+i] ^ cookieBytes[i]
			}
			return ip, port
		}

		// attributes are padded to a 4-byte boundary
		advance := 4 + int(attrLen)
		if pad := advance % 4; pad != 0 {
			advance += 4 - pad
		}
		body = body[advance:]
	}
	t.Fatal("response did not contain XOR-MAPPED-ADDRESS")
	return nil, 0
}

func TestServerRespondsToBindingRequestWithCorrectReflexiveAddress(t *testing.T) {
	srv, err := Listen("127.0.0.1:0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	client, err := net.DialUDP("udp", nil, srv.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	req, transactionID := buildBindingRequest(t)
	if _, err := client.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp := buf[:n]

	if got := resp[8:20]; string(got) != string(transactionID) {
		t.Errorf("response transaction ID = %x, want %x (must echo the request's)", got, transactionID)
	}

	gotIP, gotPort := parseXorMappedAddress(t, resp)
	wantIP := client.LocalAddr().(*net.UDPAddr).IP.To4()
	wantPort := uint16(client.LocalAddr().(*net.UDPAddr).Port)

	if !gotIP.Equal(wantIP) {
		t.Errorf("reflexive IP = %v, want %v (the client's actual source address)", gotIP, wantIP)
	}
	if gotPort != wantPort {
		t.Errorf("reflexive port = %d, want %d (the client's actual source port)", gotPort, wantPort)
	}
}

func TestServerIgnoresGarbage(t *testing.T) {
	srv, err := Listen("127.0.0.1:0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	client, err := net.DialUDP("udp", nil, srv.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("not a stun message")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	// Follow the garbage with a real request — if the server got wedged by
	// the malformed packet, this is what would prove it.
	req, transactionID := buildBindingRequest(t)
	if _, err := client.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("server did not respond to a valid request after garbage: %v", err)
	}
	if got := buf[:n][8:20]; string(got) != string(transactionID) {
		t.Errorf("response transaction ID = %x, want %x", got, transactionID)
	}
}
