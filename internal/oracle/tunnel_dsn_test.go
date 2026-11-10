package oracle

import (
	"testing"

	"github.com/godror/godror/dsn"
)

func TestTunnelDSNPreservesCredentialsAndOptions(t *testing.T) {
	for _, input := range []string{
		`user="app" password="p@ss / special" connectString="db:1521/SERVICE?connect_timeout=5" standaloneConnection=1`,
		`app/pass@//db:1521/SERVICE`,
		`user="app" password="pass" connectString="tcp://[::1]:1521/SERVICE"`,
	} {
		before, err := dsn.Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		out, err := tunnelDSN(input, "127.0.0.1:40000")
		if err != nil {
			t.Fatal(err)
		}
		after, err := dsn.Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if before.Username != after.Username || before.Password.Secret() != after.Password.Secret() || before.StandaloneConnection != after.StandaloneConnection {
			t.Fatal("credentials/options changed")
		}
		if after.ConnectString != "127.0.0.1:40000/SERVICE" && after.ConnectString != "127.0.0.1:40000/SERVICE?connect_timeout=5" {
			t.Fatalf("unexpected endpoint %s", after.ConnectString)
		}
	}
}

func TestTunnelDSNRejectsUnsupportedFormats(t *testing.T) {
	for _, target := range []string{"ALIAS", "tcps://db:1522/SERVICE", "db1,db2:1521/SERVICE", "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=db)(PORT=1521)))"} {
		input := `user="app" password="pass" connectString="` + target + `"`
		if _, err := tunnelDSN(input, "127.0.0.1:40000"); err == nil {
			t.Fatalf("accepted unsupported target %s", target)
		}
	}
}
