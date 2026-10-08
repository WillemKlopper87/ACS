package oidfallback

import (
	"context"
	"fmt"
	"github.com/gosnmp/gosnmp"
)

type SNMPReader struct{ Client *gosnmp.GoSNMP }

func (r SNMPReader) Read(ctx context.Context, oid string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	packet, err := r.Client.Get([]string{NormalizeOID(oid)})
	if err != nil {
		return "", false, err
	}
	if packet.Error != gosnmp.NoError {
		return "", false, fmt.Errorf("SNMP error: %s", packet.Error)
	}
	if len(packet.Variables) != 1 || NormalizeOID(packet.Variables[0].Name) != NormalizeOID(oid) {
		return "", false, fmt.Errorf("SNMP response does not match requested OID")
	}
	pdu := packet.Variables[0]
	switch pdu.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return "", false, nil
	}
	if raw, ok := pdu.Value.([]byte); ok {
		return string(raw), true, nil
	}
	return fmt.Sprint(pdu.Value), true, nil
}
