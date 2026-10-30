package parameters

import (
	"errors"
	"fmt"
	"regexp"
)

// Protected CPE parameters: writing any of these can cut the CPE off from
// this ACS, hand it to another controller or firmware source, change the
// operator lock, open a debug channel, or break radio regulation. None of
// these may be written by an operator, a template, a policy, a schedule, a
// bulk action or a BSS order. The few system flows that own one (credential
// rotation and graduation, XMPP provisioning) are exempted explicitly by
// jobs.AllowProtectedWrites.
//
// The list comes from the writable parameters of the field Nokia FastMile
// 1.2501.00.0798 and Beacon 3.1 discovered trees (2026-10-30), generalised
// to TR-181 and TR-098 roots. A vendor-specific (X_) item names the exact
// vendor parameter; the standard ones apply to every CPE.

// ErrProtectedParameter matches every ProtectedParameterError via errors.Is.
var ErrProtectedParameter = errors.New("protected parameter")

// ProtectedParameterError names the refused parameter and why.
type ProtectedParameterError struct {
	Name   string
	Reason string
}

func (e *ProtectedParameterError) Error() string {
	return fmt.Sprintf("parameter %q is protected and cannot be written: %s", e.Name, e.Reason)
}

func (e *ProtectedParameterError) Is(target error) bool { return target == ErrProtectedParameter }

const (
	root = `^(?:Device|InternetGatewayDevice)\.`
	inst = `\d+\.`
)

var protectedRules = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(root + `ManagementServer\.URL$`), "it points the CPE at an ACS; a wrong value cuts it off from this one"},
	{regexp.MustCompile(root + `ManagementServer\.(?:Username|Password)$`), "it is the CPE's own ACS credential, changed only by credential graduation and rotation"},
	{regexp.MustCompile(root + `ManagementServer\.ConnectionRequest(?:Username|Password)$`), "it is the Connection Request credential; use credential rotation"},
	{regexp.MustCompile(root + `ManagementServer\.EnableCWMP$`), "it switches TR-069 off on the CPE"},
	{regexp.MustCompile(root + `ManagementServer\.PeriodicInformEnable$`), "disabling periodic Informs leaves a CPE behind CGNAT with no way to reach the ACS"},
	{regexp.MustCompile(root + `ManagementServer\.UpgradesManaged$`), "it decides whether firmware may come from sources other than this ACS"},
	{regexp.MustCompile(root + `ManagementServer\.(?:ConnReqXMPPConnection|ConnReqAllowedJabberIDs|X_ALU_COM_XMPP_Enable)$`), "it is set by XMPP Connection Request provisioning"},
	{regexp.MustCompile(root + `ManagementServer\.X_[A-Za-z0-9_-]+_ACSIPv4List$`), "it restricts which addresses may act as ACS; a wrong value locks this ACS out"},
	{regexp.MustCompile(root + `(?:IP\.Interface\.` + inst + `)?X_[A-Za-z0-9_-]+_(?:Wan|Lan)AccessCfg\.Tr69Disabled$`), "it blocks TR-069 on the CPE's interfaces"},
	{regexp.MustCompile(root + `X_ALU-COM_ModemDeviceInfo\.X_ALU-COM_(?:NokiaServerURL|ForceDownloadPreconfigFromServer|ForceDownloadDeltaConfigFromServer|DeltaConfig_Flag|ImageDownloadTimer)$`), "it controls Nokia's pre-configuration and firmware source"},
	{regexp.MustCompile(root + `X_ALU-COM_ModemDeviceInfo\.X_ALU-COM_(?:BlockingTrafficByOperatorLock|QueryBOENGServerInOperatorLock|ObtainOPIDFromPLMN)$`), "it is part of the Nokia operator (network) lock, set up with Nokia's pre-configuration"},
	{regexp.MustCompile(root + `X_ALU-COM_NWCC\.(?:[A-Za-z0-9_-]+\.)*URL$`), "it points the CPE at Nokia's cloud controller; this ACS is the controller of record"},
	{regexp.MustCompile(root + `X_ALU-COM_FastmileController\.URL$`), "it points the CPE at Nokia's cloud controller; this ACS is the controller of record"},
	{regexp.MustCompile(root + `X_ALU-COM_RemoteWebDebug\.[A-Za-z0-9_-]+$`), "it opens a remote debug channel on the CPE"},
	{regexp.MustCompile(root + `WiFi\.DataElements\.Network\.ControllerID$`), "it changes the EasyMesh controller identity"},
	{regexp.MustCompile(root + `(?:WiFi\.Radio\.` + inst + `|LANDevice\.` + inst + `WLANConfiguration\.` + inst + `)RegulatoryDomain$`), "it sets the country radio rules; a wrong value breaks spectrum regulation"},
}

// ProtectedReason reports whether a parameter may never be written by an
// operator-, template-, policy-, schedule-, bulk- or BSS-driven job.
func ProtectedReason(name string) (string, bool) {
	for _, rule := range protectedRules {
		if rule.re.MatchString(name) {
			return rule.reason, true
		}
	}
	return "", false
}

// CheckProtected returns a *ProtectedParameterError for the first protected
// name, or nil.
func CheckProtected(names ...string) error {
	for _, name := range names {
		if reason, ok := ProtectedReason(name); ok {
			return &ProtectedParameterError{Name: name, Reason: reason}
		}
	}
	return nil
}
