package oracle

import (
	"fmt"
	"regexp"

	"github.com/godror/godror/dsn"
)

// Only a single TCP Easy Connect target is supported. Never silently flatten
// TNS aliases, TCPS/wallets, DESCRIPTIONs or multi-host/failover descriptors.
var tunnelEasyConnect = regexp.MustCompile(`(?i)^(?://|tcp://)?(?:\[[^\]]+\]|[^/:,()?\s]+)(?::[0-9]+)?(/[^?\s]+)(\?[^\s]*)?$`)

func tunnelDSN(original, localAddress string) (string, error) {
	p, err := dsn.Parse(original)
	if err != nil {
		return "", fmt.Errorf("invalid database DSN for SSH connection")
	}
	parts := tunnelEasyConnect.FindStringSubmatch(p.ConnectString)
	if parts == nil {
		return "", fmt.Errorf("SSH requires a single TCP Easy Connect DSN (host:port/service); TNS aliases, TCPS and multi-host descriptors are unsupported")
	}
	p.ConnectString = localAddress + parts[1] + parts[2]
	return p.StringWithPassword(), nil
}
