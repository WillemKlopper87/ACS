package wifiinsights

import (
	"bufio"
	"encoding/csv"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
)

// PrivateMAC reports a locally administered (randomised) MAC: bit 1 of the
// first octet, i.e. a second hex digit of 2, 6, A or E. Android and iOS use
// these per network by default, so most phones show up this way and no
// vendor can be derived from them.
func PrivateMAC(mac string) bool {
	if len(mac) < 2 {
		return false
	}
	switch mac[1] {
	case '2', '6', 'a', 'e', 'A', 'E':
		return true
	}
	return false
}

// OUIRegistry maps a 24-bit OUI ("a4:fc:77") to its IEEE organisation name.
type OUIRegistry struct {
	byPrefix map[string]string
}

// LoadOUIRegistry reads the IEEE MA-L registry CSV (oui.csv:
// Registry,Assignment,Organization Name,Organization Address).
func LoadOUIRegistry(r io.Reader) (*OUIRegistry, error) {
	reader := csv.NewReader(bufio.NewReader(r))
	reader.FieldsPerRecord = -1
	reg := &OUIRegistry{byPrefix: map[string]string{}}
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) < 3 || len(rec[1]) != 6 {
			continue
		}
		mac := NormalizeMAC(rec[1] + "000000")
		if mac == "" {
			continue
		}
		reg.byPrefix[mac[:8]] = strings.TrimSpace(rec[2])
	}
	return reg, nil
}

// Vendor returns the organisation for a MAC, "Private" for a randomised
// MAC, or "" when unknown (or no registry is loaded).
func (r *OUIRegistry) Vendor(mac string) string {
	if PrivateMAC(mac) {
		return "Private"
	}
	if r == nil || len(mac) < 8 {
		return ""
	}
	return r.byPrefix[mac[:8]]
}

var (
	ouiOnce sync.Once
	ouiReg  *OUIRegistry
)

// DefaultOUIRegistry loads ACS_OUI_CSV once (the IEEE oui.csv, e.g. from
// https://standards-oui.ieee.org/oui/oui.csv). Without it, vendors are
// reported only as "Private" or unknown.
func DefaultOUIRegistry() *OUIRegistry {
	ouiOnce.Do(func() {
		path := strings.TrimSpace(os.Getenv("ACS_OUI_CSV"))
		if path == "" {
			return
		}
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		ouiReg, _ = LoadOUIRegistry(f)
	})
	return ouiReg
}

var osHints = []struct {
	re *regexp.Regexp
	os string
}{
	{regexp.MustCompile(`(?i)(iphone|ipad|ipod)`), "iOS"},
	{regexp.MustCompile(`(?i)^(macbook|imac|mac-?mini|mbp)`), "macOS"},
	{regexp.MustCompile(`(?i)^(android|galaxy|sm-[a-z0-9]+|redmi|xiaomi|poco|huawei|honor|oppo|vivo|realme|pixel|oneplus|nokia-|moto|tecno|infinix|itel)`), "Android"},
	{regexp.MustCompile(`(?i)^(desktop-|laptop-|win-)`), "Windows"},
}

// InferOS guesses a client OS from its DHCP hostname. CWMP exposes no DHCP
// option-55/60 fingerprint on the field CPEs, so this is a heuristic and
// the API labels it "inferred". Unknown hostnames return "Other".
func InferOS(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	for _, h := range osHints {
		if h.re.MatchString(hostname) {
			return h.os
		}
	}
	return "Other"
}
