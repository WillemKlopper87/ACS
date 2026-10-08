// oidprobe is an explicit, read-only SNMPv3 fallback tool for qualified packs.
// It connects only to the operator-supplied device; it does not scan a subnet.
package main

import (
	"acs/internal/oidfallback"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gosnmp/gosnmp"
	"os"
	"time"
)

func run() error {
	target := flag.String("target", "", "device IP or hostname")
	port := flag.Uint("port", 161, "SNMP UDP port")
	profileFile := flag.String("profile", "", "firmware-qualified OID profile JSON")
	identityFile := flag.String("identity", "", "device vendor/model/firmware identity JSON")
	output := flag.String("output", "oid-discovery.json", "result file")
	flag.Parse()
	if *target == "" || *profileFile == "" || *identityFile == "" || *port == 0 || *port > 65535 {
		return fmt.Errorf("target, profile, identity and valid port are required")
	}
	var profile oidfallback.Profile
	var identity oidfallback.Identity
	raw, err := os.ReadFile(*profileFile)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return err
	}
	raw, err = os.ReadFile(*identityFile)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		return err
	}
	if err := profile.Validate(identity); err != nil {
		return err
	}
	username, authPassword, privPassword := os.Getenv("ACS_SNMP_USERNAME"), os.Getenv("ACS_SNMP_AUTH_PASSWORD"), os.Getenv("ACS_SNMP_PRIV_PASSWORD")
	if username == "" || len(authPassword) < 8 || len(privPassword) < 8 {
		return fmt.Errorf("SNMPv3 username and auth/privacy passwords (minimum 8 bytes) are required in ACS_SNMP_* environment variables")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := &gosnmp.GoSNMP{Target: *target, Port: uint16(*port), Version: gosnmp.Version3, Timeout: 2 * time.Second, Retries: 1, MaxOids: 1, Context: ctx, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv, SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: username, AuthenticationProtocol: gosnmp.SHA256, AuthenticationPassphrase: authPassword, PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: privPassword}}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("SNMP connection failed")
	}
	defer client.Conn.Close()
	reader := oidfallback.SNMPReader{Client: client}
	actual, present, err := reader.Read(ctx, "1.3.6.1.2.1.1.2.0")
	if err != nil || !present {
		return fmt.Errorf("device sysObjectID could not be verified")
	}
	identity.SysObjectID = actual
	results, err := oidfallback.Resolve(ctx, profile, identity, nil, nil, reader)
	if err != nil {
		return err
	}
	report := map[string]any{"identity": identity, "profile_id": profile.ID, "evidence": profile.Evidence, "collected_at": time.Now().UTC(), "fields": results}
	raw, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, raw, 0600); err != nil {
		return err
	}
	for _, reading := range results {
		if !reading.Available {
			return fmt.Errorf("output saved, but one or more metrics were unavailable")
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
