package connreq

import (
	"context"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAnnexGDatagramFormat(t *testing.T) {
	b := BuildAnnexGDatagram("203.0.113.9:30000", "cr-user", "s3cret", time.Unix(1700000000, 0))
	s := string(b)
	re := regexp.MustCompile(`^GET http://203\.0\.113\.9:30000\?ts=1700000000&id=(\d+)&un=cr-user&cn=([0-9a-f]{16})&sig=([0-9a-f]{40}) HTTP/1\.1\r\nHost: 203\.0\.113\.9:30000\r\n\r\n$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("datagram does not match Annex G.2.2 shape:\n%q", s)
	}
	if want := SignAnnexG("s3cret", "1700000000", m[1], "cr-user", m[2]); m[3] != want {
		t.Errorf("sig = %s, want HMAC-SHA1(password, ts+id+un+cn) = %s", m[3], want)
	}
}

func TestSendUDPDeliversRepeatedDatagrams(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := SendUDP(ctx, pc.LocalAddr().String(), "u", "p"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	got := 0
	for got < udpSendCount {
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		if !strings.HasPrefix(string(buf[:n]), "GET http://") {
			t.Fatalf("unexpected payload %q", buf[:n])
		}
		got++
	}
	if got != udpSendCount {
		t.Errorf("received %d datagrams, want %d", got, udpSendCount)
	}
}

func TestSendUDPRejectsBadAddress(t *testing.T) {
	if err := SendUDP(context.Background(), "", "u", "p"); err == nil {
		t.Error("empty address accepted")
	}
	if err := SendUDP(context.Background(), "no-port", "u", "p"); err == nil {
		t.Error("address without port accepted")
	}
}
