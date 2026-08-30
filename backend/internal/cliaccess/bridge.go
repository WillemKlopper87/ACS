// Bridging a browser-side terminal to a device's real SSH/Telnet port over
// a WebSocket — the two directions (ws->device, device->ws) run as
// independent goroutines so either side can be the one that closes first.
package cliaccess

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// dialTimeout bounds how long a connect attempt waits before giving up —
// generous enough for a real network hop, short enough that a CGNAT'd
// device (currently unreachable — see the package doc comment) fails fast
// and visibly rather than hanging the browser tab.
const dialTimeout = 10 * time.Second

// BridgeSSH dials cred's host:port over SSH, requests an interactive PTY,
// and pipes bytes bidirectionally between the session and rw (a WebSocket
// connection from the caller's point of view — this function only needs
// io.ReadWriter, so it's testable against an in-memory pipe without a real
// browser). Blocks until either side closes or ctx is canceled.
//
// hostKey verifies the device's host key (audit P0.4) — callers pass
// Repository.TOFUHostKeyCallback(ctx, deviceID), which pins the key on
// first connect and rejects any later mismatch, replacing the previous
// InsecureIgnoreHostKey behavior that accepted an interceptor silently.
func BridgeSSH(ctx context.Context, cred *Credential, rw io.ReadWriter, hostKey ssh.HostKeyCallback) error {
	config := &ssh.ClientConfig{
		User:            cred.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(cred.Password)},
		HostKeyCallback: hostKey,
		Timeout:         dialTimeout,
	}

	addr := net.JoinHostPort(cred.Host, fmt.Sprintf("%d", cred.Port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer session.Close()

	// 80x24 is the universal terminal default — the console panel doesn't
	// negotiate real dimensions yet (a WINDOW_CHANGE-on-resize wire-up is a
	// natural follow-up once this is being used against a reachable device).
	if err := session.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		return fmt.Errorf("request pty: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("open stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open stdout pipe: %w", err)
	}
	session.Stderr = session.Stdout // one terminal stream, same as a real interactive shell

	if err := session.Shell(); err != nil {
		return fmt.Errorf("start shell: %w", err)
	}

	return pipeBidirectional(ctx, rw, stdin, stdout, func() error { return session.Close() })
}

// BridgeTelnet dials cred's host:port over plain TCP and speaks just
// enough Telnet (RFC 854) to be usable against a simple embedded device:
// every option negotiation (IAC WILL/WONT/DO/DONT/SB) is stripped and
// answered with a blanket refusal (IAC WONT / IAC DONT) rather than
// implementing the option itself — correct per RFC 854 ("a machine ...
// must respond ... refusing" is always a valid response), and sufficient
// for the common case of a CPE's minimal telnetd that just wants line mode.
// Anything relying on a specific negotiated option (character-mode/
// echo-suppression from the server side) may behave oddly — a real gap,
// not hidden here.
func BridgeTelnet(ctx context.Context, cred *Credential, rw io.ReadWriter) error {
	addr := net.JoinHostPort(cred.Host, fmt.Sprintf("%d", cred.Port))
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return fmt.Errorf("telnet dial %s: %w", addr, err)
	}
	defer conn.Close()

	fromDevice := &telnetFilterReader{r: conn, w: conn}

	return pipeBidirectional(ctx, rw, conn, fromDevice, func() error { return conn.Close() })
}

// pipeBidirectional copies rw->deviceIn and deviceOut->rw concurrently,
// returning once either direction ends (EOF or error) — at which point it
// closes the device-side connection (via closeDevice) so the other
// goroutine unblocks too, and reports whichever error ended things first.
func pipeBidirectional(ctx context.Context, rw io.ReadWriter, deviceIn io.Writer, deviceOut io.Reader, closeDevice func() error) error {
	done := make(chan error, 2)

	go func() {
		_, err := io.Copy(deviceIn, rw)
		done <- err
	}()
	go func() {
		_, err := io.Copy(rw, deviceOut)
		done <- err
	}()

	select {
	case err := <-done:
		_ = closeDevice()
		return err
	case <-ctx.Done():
		_ = closeDevice()
		return ctx.Err()
	}
}
