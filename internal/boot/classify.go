package boot

import (
	"errors"
	"net"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/ivancarlosti/up/internal/i18n"
)

// Classify maps a database error to the stable code the frontend translates,
// plus the developer oriented detail that goes with it (the "detail" field of
// the API error body).
//
// The point is telling two situations apart that used to look identical in the
// browser: the connection never reached the server (DB_HOST, DB_PORT, the
// server is down, a firewall) and the server answered but refused the
// credentials or the database name (DB_USERNAME, DB_PASSWORD, DB_DATABASE).
// Both used to end in the same 90 seconds of "database not ready yet, retrying"
// followed by an exit, with nothing in the browser at all.
//
// It returns an empty code when the error is not a database failure (a
// validation error, a cancelled context): the caller knows better what to
// answer with.
func Classify(err error) (code string, detail string) {
	if err == nil {
		return "", ""
	}
	detail = err.Error()

	// Server side errors: the MariaDB/MySQL error number is the only reliable
	// signal, so the driver type is matched instead of the message.
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1044, 1045, 1130, 1698:
			// ER_DBACCESS_DENIED_ERROR, ER_ACCESS_DENIED_ERROR,
			// ER_HOST_NOT_PRIVILEGED, ER_ACCESS_DENIED_NO_PASSWORD_ERROR.
			return i18n.CodeDatabaseCredentials, detail
		case 1049:
			// ER_BAD_DB_ERROR.
			return i18n.CodeDatabaseMissing, detail
		case 1042, 1142, 1143, 1227:
			// ER_BAD_HOST_ERROR, ER_TABLEACCESS_DENIED_ERROR,
			// ER_COLUMNACCESS_DENIED_ERROR, ER_SPECIFIC_ACCESS_DENIED_ERROR:
			// the connection works but the account may not change the schema,
			// which is what the automatic migrations need.
			return i18n.CodeDatabaseMigration, detail
		default:
			return i18n.CodeDatabase, detail
		}
	}

	// Client side failures. The dial errors of go-sql-driver (*net.OpError),
	// the DNS failures (*net.DNSError), the DSN timeout and an expired context
	// all satisfy net.Error.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return i18n.CodeDatabaseUnreachable, detail
	}
	// A pooled connection the server closed mid flight comes back as a plain
	// driver error, so the fragments are the last resort.
	lowered := strings.ToLower(detail)
	for _, fragment := range unreachableFragments {
		if strings.Contains(lowered, fragment) {
			return i18n.CodeDatabaseUnreachable, detail
		}
	}
	return "", detail
}

// unreachableFragments are the driver messages that mean "the connection is
// gone" without carrying a typed error to match on.
var unreachableFragments = []string{
	"invalid connection",
	"bad connection",
	"connection refused",
	"connection reset",
	"broken pipe",
	"i/o timeout",
	"no such host",
	"server has gone away",
	"lost connection",
}
