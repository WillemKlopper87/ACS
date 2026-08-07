package cliaccess

import "io"

// Telnet command bytes (RFC 854).
const (
	tnSE   = 240
	tnSB   = 250
	tnWILL = 251
	tnWONT = 252
	tnDO   = 253
	tnDONT = 254
	tnIAC  = 255
)

// telnetFilterReader wraps a device's raw Telnet stream, stripping IAC
// option-negotiation/subnegotiation sequences and answering every
// WILL/DO with a blanket refusal (DONT/WONT — see BridgeTelnet's doc
// comment for why that's a spec-legal response, not a shortcut) so the
// terminal on the other end of w only ever sees real terminal output.
type telnetFilterReader struct {
	r io.Reader // the device connection, read one byte at a time
	w io.Writer // where negotiation replies get written — the same device connection
}

func (t *telnetFilterReader) readByte() (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(t.r, b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

func (t *telnetFilterReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		b, err := t.readByte()
		if err != nil {
			if n > 0 {
				return n, nil // hand back what we've decoded so far; the error resurfaces on the next call
			}
			return 0, err
		}

		if b != tnIAC {
			p[n] = b
			n++
			continue
		}

		cmd, err := t.readByte()
		if err != nil {
			return n, err
		}
		switch cmd {
		case tnIAC: // escaped literal 0xFF
			p[n] = tnIAC
			n++
		case tnWILL, tnDO:
			opt, err := t.readByte()
			if err != nil {
				return n, err
			}
			reply := byte(tnDONT)
			if cmd == tnDO {
				reply = tnWONT
			}
			if _, err := t.w.Write([]byte{tnIAC, reply, opt}); err != nil {
				return n, err
			}
		case tnWONT, tnDONT:
			if _, err := t.readByte(); err != nil { // consume the option byte, no reply needed
				return n, err
			}
		case tnSB:
			// Consume everything up to and including IAC SE.
			for {
				b, err := t.readByte()
				if err != nil {
					return n, err
				}
				if b != tnIAC {
					continue
				}
				b2, err := t.readByte()
				if err != nil {
					return n, err
				}
				if b2 == tnSE {
					break
				}
			}
		default:
			// Single-byte commands (NOP, GA, etc.) — nothing further to consume.
		}
	}
	return n, nil
}
