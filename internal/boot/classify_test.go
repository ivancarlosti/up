package boot

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/ivancarlosti/up/internal/i18n"
)

// TestClassifySeparatesTheThreeFailures is the point of the whole change set:
// before it, a refused connection, a rejected password and a missing database
// were the same line in the browser (nothing at all, after 90 seconds of
// retrying). Every case below is what the driver really returns.
func TestClassifySeparatesTheThreeFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "server unreachable (dial refused)",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			},
			want: i18n.CodeDatabaseUnreachable,
		},
		{
			name: "server unreachable (dial timeout)",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ETIMEDOUT},
			},
			want: i18n.CodeDatabaseUnreachable,
		},
		{
			name: "server unreachable (unknown host)",
			err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{
				Err: "no such host", Name: "db.internal", IsNotFound: true,
			}},
			want: i18n.CodeDatabaseUnreachable,
		},
		{
			name: "server unreachable (pooled connection died)",
			err:  errors.New("invalid connection"),
			want: i18n.CodeDatabaseUnreachable,
		},
		{
			name: "wrong credentials",
			err:  &mysql.MySQLError{Number: 1045, Message: "Access denied for user 'up'@'10.0.0.4' (using password: YES)"},
			want: i18n.CodeDatabaseCredentials,
		},
		{
			name: "wrong credentials (database level grant)",
			err:  &mysql.MySQLError{Number: 1044, Message: "Access denied for user 'up'@'10.0.0.4' to database 'up'"},
			want: i18n.CodeDatabaseCredentials,
		},
		{
			name: "wrong credentials (host not allowed)",
			err:  &mysql.MySQLError{Number: 1130, Message: "Host '10.0.0.4' is not allowed to connect"},
			want: i18n.CodeDatabaseCredentials,
		},
		{
			name: "missing database",
			err:  &mysql.MySQLError{Number: 1049, Message: "Unknown database 'up'"},
			want: i18n.CodeDatabaseMissing,
		},
		{
			name: "migration without DDL rights",
			err:  fmt.Errorf("running automatic migrations: %w", &mysql.MySQLError{Number: 1142, Message: "CREATE command denied to user 'up'@'10.0.0.4' for table 'settings'"}),
			want: i18n.CodeDatabaseMigration,
		},
		{
			name: "another server side failure",
			err:  &mysql.MySQLError{Number: 1040, Message: "Too many connections"},
			want: i18n.CodeDatabase,
		},
		{
			name: "not a database failure at all",
			err:  errors.New("monitor 42 does not exist"),
			want: "",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			code, detail := Classify(test.err)
			if code != test.want {
				t.Fatalf("Classify(%v) = %q, want %q", test.err, code, test.want)
			}
			if detail != test.err.Error() {
				t.Fatalf("the detail must carry the driver message: got %q", detail)
			}
		})
	}
}

// TestClassifyNil keeps the "no error, no code" contract the callers rely on
// (a nil error must never be reported as a failure).
func TestClassifyNil(t *testing.T) {
	if code, detail := Classify(nil); code != "" || detail != "" {
		t.Fatalf("Classify(nil) = %q, %q, want empty", code, detail)
	}
}
