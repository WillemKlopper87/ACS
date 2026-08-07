package cliaccess

import (
	"bytes"
	"io"
	"testing"
)

// fakeConn lets the reply-writer and the data-source be inspected/fed
// independently in a test, without a real socket.
type fakeConn struct {
	in  *bytes.Reader // what the "device" sent
	out bytes.Buffer  // what we wrote back (negotiation replies)
}

func (f *fakeConn) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeConn) Write(p []byte) (int, error) { return f.out.Write(p) }

func TestTelnetFilterStripsNegotiationAndPassesDataThrough(t *testing.T) {
	// IAC WILL ECHO(1), "hello ", IAC DO SUPPRESS_GA(3), "world", IAC WONT LINEMODE(34) (no reply expected), IAC IAC (literal 0xFF)
	raw := []byte{
		tnIAC, tnWILL, 1,
	}
	raw = append(raw, []byte("hello ")...)
	raw = append(raw, tnIAC, tnDO, 3)
	raw = append(raw, []byte("world")...)
	raw = append(raw, tnIAC, tnWONT, 34)
	raw = append(raw, tnIAC, tnIAC)

	conn := &fakeConn{in: bytes.NewReader(raw)}
	filter := &telnetFilterReader{r: conn, w: conn}

	got, err := io.ReadAll(filter)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	want := "hello world" + string([]byte{0xFF})
	if string(got) != want {
		t.Errorf("filtered output = %q, want %q", got, want)
	}

	wantReplies := []byte{tnIAC, tnDONT, 1, tnIAC, tnWONT, 3} // WILL ECHO -> DONT ECHO; DO SGA -> WONT SGA; WONT LINEMODE needs no reply
	if !bytes.Equal(conn.out.Bytes(), wantReplies) {
		t.Errorf("negotiation replies = % x, want % x", conn.out.Bytes(), wantReplies)
	}
}

func TestTelnetFilterConsumesSubnegotiation(t *testing.T) {
	// IAC SB <opt> <junk bytes, including a lone IAC that's NOT followed by SE> IAC SE, then real data.
	raw := []byte{tnIAC, tnSB, 24, 0, 'x', 't', 'e', 'r', 'm', tnIAC, tnSE}
	raw = append(raw, []byte("ready")...)

	conn := &fakeConn{in: bytes.NewReader(raw)}
	filter := &telnetFilterReader{r: conn, w: conn}

	got, err := io.ReadAll(filter)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "ready" {
		t.Errorf("filtered output = %q, want %q (subnegotiation block should be fully consumed)", got, "ready")
	}
	if conn.out.Len() != 0 {
		t.Errorf("expected no reply to a subnegotiation block, got % x", conn.out.Bytes())
	}
}
