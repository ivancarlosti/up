package services

import "strings"

// isDuplicateKey reports whether an error is a unique index violation.
//
// The MySQL driver is not built with TranslateError, so GORM hands over the raw
// server error instead of gorm.ErrDuplicatedKey: error 1062 is what an insert of a
// name that is already taken produces. Matching on the number (and not on the
// message, which is localized by the server) keeps this readable across MariaDB
// and MySQL.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Error 1062") ||
		strings.Contains(strings.ToLower(err.Error()), "duplicate entry")
}
