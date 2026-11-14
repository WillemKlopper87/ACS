package auth

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // the range API is keyed by SHA-1; this is a lookup key, not protection
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// BreachCheckURLEnv turns the breached-password lookup on. Its value is
// the base URL of a "range" endpoint in the Have I Been Pwned format:
// https://api.pwnedpasswords.com/range/ for the public service, or the
// address of a copy hosted inside the network. Unset, no lookup is made.
const BreachCheckURLEnv = "ACS_PASSWORD_BREACH_CHECK_URL"

// ErrPasswordBreached: the password is in a published breach.
var ErrPasswordBreached = errors.New("this password appears in published data breaches, so attackers already try it; choose another")

// BreachChecker asks a range service whether a password has appeared in
// a published breach (security assessment O12).
//
// What leaves this process is the first five hex characters of the
// password's SHA-1 digest, nothing else: the service answers with every
// digest suffix that shares that prefix (hundreds of them) and the match
// is made here. The service cannot tell which of them was asked about,
// nor whether any matched. It is still an outbound call that depends on
// something outside the ACS, which is why it is off unless configured,
// and why a failed lookup lets the password through (the other rules
// have already been applied) rather than blocking every password change
// when the service cannot be reached.
type BreachChecker struct {
	base   string
	client *http.Client
}

// NewBreachChecker returns a checker for the range endpoint at base, or
// nil when base is empty (the lookup is off).
func NewBreachChecker(base string) (*BreachChecker, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return nil, nil
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%s must be an http(s) URL, such as https://api.pwnedpasswords.com/range/", BreachCheckURLEnv)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return &BreachChecker{base: base, client: &http.Client{Timeout: 4 * time.Second}}, nil
}

// Check returns ErrPasswordBreached when the password is known to the
// service, nil when it is not, and any other error when the lookup could
// not be made. A nil checker always returns nil.
func (c *BreachChecker) Check(ctx context.Context, password string) error {
	if c == nil {
		return nil
	}
	sum := sha1.Sum([]byte(password)) //nolint:gosec
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := digest[:5], digest[5:]

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+prefix, nil)
	if err != nil {
		return fmt.Errorf("breach lookup: %w", err)
	}
	// Ask for a padded answer, so its size does not hint at the prefix.
	req.Header.Set("Add-Padding", "true")
	req.Header.Set("User-Agent", "acs-password-check")
	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("breach lookup: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("breach lookup: the service answered %d", res.StatusCode)
	}
	scan := bufio.NewScanner(res.Body)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		got, count, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(got, suffix) {
			continue
		}
		// Padding rows carry a count of 0: not a real match.
		if n, err := strconv.Atoi(strings.TrimSpace(count)); err == nil && n > 0 {
			return ErrPasswordBreached
		}
	}
	if err := scan.Err(); err != nil {
		return fmt.Errorf("breach lookup: %w", err)
	}
	return nil
}
