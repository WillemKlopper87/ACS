package connreq

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Annex G UDP Connection Request (TR-069 Amendment 2+ Annex G, "STUN and
// UDP Connection Requests"). For a CPE behind NAT the ACS cannot open a
// TCP connection to ConnectionRequestURL; instead it sends a small UDP
// datagram to the reflexive address the device learned via STUN and
// reported in ManagementServer.UDPConnectionRequestAddress. The payload
// is an HTTP/1.1 GET request line (G.2.2.1):
//
//	GET http://<addr>?ts=<ts>&id=<id>&un=<un>&cn=<cn>&sig=<sig> HTTP/1.1
//	Host: <addr>
//
// where ts is a timestamp, id a message id, un the
// ConnectionRequestUsername, cn a random nonce, and sig the lowercase
// hex HMAC-SHA1 over the concatenation ts+id+un+cn keyed with the
// ConnectionRequestPassword (G.2.2.2). UDP is lossy, so the datagram is
// sent udpSendCount times (G.2.2.1 allows repeats; the CPE ignores
// duplicates by id/ts). Note: this codebase has not been validated
// against a real STUN-capable CPE — the format is implemented from the
// specification text and marked as such in the compatibility matrix.

const (
	udpSendCount    = 3
	udpSendInterval = 100 * time.Millisecond
)

// SignAnnexG computes the Annex G signature for the given fields.
func SignAnnexG(password, ts, id, un, cn string) string {
	mac := hmac.New(sha1.New, []byte(password))
	mac.Write([]byte(ts + id + un + cn))
	return hex.EncodeToString(mac.Sum(nil))
}

// BuildAnnexGDatagram renders the UDP payload for addr (host:port).
func BuildAnnexGDatagram(addr, username, password string, now time.Time) []byte {
	ts := strconv.FormatInt(now.Unix(), 10)
	idb := make([]byte, 4)
	_, _ = rand.Read(idb)
	id := strconv.FormatUint(uint64(uint32(idb[0])<<24|uint32(idb[1])<<16|uint32(idb[2])<<8|uint32(idb[3])), 10)
	cnb := make([]byte, 8)
	_, _ = rand.Read(cnb)
	cn := hex.EncodeToString(cnb)
	sig := SignAnnexG(password, ts, id, username, cn)
	return []byte(fmt.Sprintf("GET http://%s?ts=%s&id=%s&un=%s&cn=%s&sig=%s HTTP/1.1\r\nHost: %s\r\n\r\n",
		addr, ts, id, username, cn, sig, addr))
}

// SendUDP sends the Annex G wake-up datagram to addr. It reports only
// that the datagrams left this host — UDP gives no delivery signal; the
// caller must watch for the CPE's subsequent Inform (EventCode 6).
func SendUDP(ctx context.Context, addr, username, password string) error {
	if addr == "" {
		return fmt.Errorf("no UDP connection request address")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("invalid UDP connection request address %q: %w", addr, err)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return fmt.Errorf("udp dial %s: %w", addr, err)
	}
	defer conn.Close()

	payload := BuildAnnexGDatagram(addr, username, password, time.Now())
	for i := 0; i < udpSendCount; i++ {
		if _, err := conn.Write(payload); err != nil {
			return fmt.Errorf("udp send %s: %w", addr, err)
		}
		if i < udpSendCount-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(udpSendInterval):
			}
		}
	}
	return nil
}
