package xmpp

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

// XEP-0133 (Service Administration) command nodes. Prosody implements both
// in its bundled mod_admin_adhoc, available to accounts listed in its
// `admins` option — which is how the ACS creates one XMPP account per CPE
// over the session it already holds, with no docker socket, shell access
// or extra HTTP admin surface (see docs/XMPP-CONNECTION-REQUEST.md).
const (
	nodeAddUser        = "http://jabber.org/protocol/admin#add-user"
	nodeChangePassword = "http://jabber.org/protocol/admin#change-user-password"
	formTypeAdmin      = "http://jabber.org/protocol/admin"
)

// EnsureAccount creates accountJID with password on the ACS's own server,
// or resets its password when it already exists — idempotent, so a
// provisioning attempt that is retried after a crash converges on the same
// account. accountJID must be a bare JID on the client's domain.
func (c *Client) EnsureAccount(ctx context.Context, accountJID, password string) error {
	if Bare(accountJID) != accountJID || !strings.HasSuffix(strings.ToLower(accountJID), "@"+strings.ToLower(c.cfg.Domain)) {
		return fmt.Errorf("%w: %s is not a bare JID on %s", ErrInvalidJID, accountJID, c.cfg.Domain)
	}
	addErr := c.adminCommand(ctx, nodeAddUser, [][2]string{
		{"accountjid", accountJID}, {"password", password}, {"password-verify", password},
	})
	if addErr == nil {
		return nil
	}
	if ctx.Err() != nil || errors.Is(addErr, ErrNotConnected) {
		return addErr
	}
	// The usual reason add-user fails is "Account already exists" (a
	// retried provisioning); resetting the password then makes the
	// account match the credential the ACS is about to push to the CPE.
	if err := c.adminCommand(ctx, nodeChangePassword, [][2]string{
		{"accountjid", accountJID}, {"password", password},
	}); err != nil {
		return fmt.Errorf("create account: %v; reset password: %w", addErr, err)
	}
	return nil
}

type adhocCommand struct {
	XMLName   xml.Name  `xml:"http://jabber.org/protocol/commands command"`
	Node      string    `xml:"node,attr"`
	Action    string    `xml:"action,attr,omitempty"`
	SessionID string    `xml:"sessionid,attr,omitempty"`
	Status    string    `xml:"status,attr,omitempty"`
	Form      *dataForm `xml:"jabber:x:data x,omitempty"`
	Notes     []cmdNote `xml:"note"`
}

type cmdNote struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type dataForm struct {
	Type   string      `xml:"type,attr"`
	Fields []formField `xml:"field"`
}

type formField struct {
	Var    string   `xml:"var,attr"`
	Type   string   `xml:"type,attr,omitempty"`
	Values []string `xml:"value"`
}

// adminCommand runs a two-step XEP-0050 command: execute (the server
// returns a form and a session id), then submit the filled form. Field
// values (the account password) only ever travel inside the TLS stream.
func (c *Client) adminCommand(ctx context.Context, node string, fields [][2]string) error {
	first, err := xml.Marshal(adhocCommand{Node: node, Action: "execute"})
	if err != nil {
		return err
	}
	resp, err := c.SendIQ(ctx, "set", c.cfg.Domain, first)
	if err != nil {
		return fmt.Errorf("%s: %w", node, err)
	}
	var started adhocCommand
	if err := xml.Unmarshal(resp.Payload, &started); err != nil {
		return fmt.Errorf("%s: parse response: %w", node, err)
	}
	if err := noteError(started); err != nil {
		return fmt.Errorf("%s: %w", node, err)
	}
	if started.Status == "completed" {
		return nil
	}
	if started.SessionID == "" {
		return fmt.Errorf("%s: server returned no command session", node)
	}

	form := &dataForm{Type: "submit", Fields: []formField{{Var: "FORM_TYPE", Type: "hidden", Values: []string{formTypeAdmin}}}}
	for _, f := range fields {
		form.Fields = append(form.Fields, formField{Var: f[0], Values: []string{f[1]}})
	}
	second, err := xml.Marshal(adhocCommand{Node: node, Action: "complete", SessionID: started.SessionID, Form: form})
	if err != nil {
		return err
	}
	resp, err = c.SendIQ(ctx, "set", c.cfg.Domain, second)
	if err != nil {
		return fmt.Errorf("%s: %w", node, err)
	}
	var done adhocCommand
	if err := xml.Unmarshal(resp.Payload, &done); err != nil {
		return fmt.Errorf("%s: parse response: %w", node, err)
	}
	if err := noteError(done); err != nil {
		return fmt.Errorf("%s: %w", node, err)
	}
	if done.Status != "completed" {
		return fmt.Errorf("%s: command ended with status %q", node, done.Status)
	}
	return nil
}

func noteError(cmd adhocCommand) error {
	for _, n := range cmd.Notes {
		if n.Type == "error" {
			return fmt.Errorf("server reported: %s", strings.TrimSpace(n.Text))
		}
	}
	return nil
}
