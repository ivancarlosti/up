package models

import "strings"

// ---------------------------------------------------------------------------
// Built-in WHOIS rules (the TLDs without RDAP)
// ---------------------------------------------------------------------------

// DefaultWhoisDateLayouts is the recommended value of
// WhoisParser.DateLayouts, and the value a new rule starts with in the admin
// form: ISO 8601.
//
// The two layouts cover the two shapes a registry actually writes: a full
// timestamp (2026-11-22T01:38:41Z) and a plain date (2026-11-22). They are Go
// reference layouts, so a registry that adds an offset or a "Z" keeps working
// without touching the rule.
const DefaultWhoisDateLayouts = "2006-01-02T15:04:05Z07:00;2006-01-02"

// DefaultWhoisNotFoundPattern is the shared "the domain is free" pattern a
// built-in rule starts with. It only carries phrases that unambiguously mean
// "not registered", so a registered domain is never reported as free.
const DefaultWhoisNotFoundPattern = `(?i)(no match for|not found|no entries found|no object found|not registered|no data found|no information available|domain status:\s*free|status:\s*free|is free)`

// The Go reference layouts the built-in rules use (one name per registry shape).
const (
	// layoutISO8601 is ISO 8601 date and time, with or without an offset.
	layoutISO8601 = "2006-01-02T15:04:05Z07:00"
	// layoutISO8601Millis is ISO 8601 with fractional seconds (.my).
	layoutISO8601Millis = "2006-01-02T15:04:05.000Z07:00"
	// layoutDate is a plain calendar date.
	layoutDate = "2006-01-02"
	// layoutDateTime is a date followed by a time (.cn).
	layoutDateTime = "2006-01-02 15:04:05"
	// layoutDateTimeZone is a date, a time and a zone abbreviation (.cl).
	layoutDateTimeZone = "2006-01-02 15:04:05 MST"
	// layoutDotDateTime is "02.01.2006 15:04:05" (.rs).
	layoutDotDateTime = "02.01.2006 15:04:05"
	// layoutSlashDateTime is "02/01/2006 15:04:05" (.pt).
	layoutSlashDateTime = "02/01/2006 15:04:05"
	// layoutDayMonthYear is "02-01-2006" (.hk).
	layoutDayMonthYear = "02-01-2006"
	// layoutDotDate is "02.01.2006" (.ax, .ls, .mk, .ve).
	layoutDotDate = "02.01.2006"
	// layoutDayMonthShort is "02-Jan-2006", the shape .edu uses.
	layoutDayMonthShort = "02-Jan-2006"
	// layoutYearMonthShort is "2006-Jan-02", the shape the .tr registry uses.
	layoutYearMonthShort = "2006-Jan-02"
	// layoutDayMonthShortDateTime is "02-Jan-2006 15:04:05", used by .bn.
	layoutDayMonthShortDateTime = "02-Jan-2006 15:04:05"
)

// Shared expressions of the IANA-style registries (most ccTLDs).
const (
	whoisRegexRegistryExpiry = `(?im)^\s*Registry Expiry Date:\s*(\S+)`
	whoisRegexExpirationDate = `(?im)^\s*Expiration Date:\s*(\S+)`
	whoisRegexExpireDate     = `(?im)^\s*Expire Date:\s*(\S+)`
	whoisRegexExpires        = `(?im)^\s*Expires?:\s*(\S+)`
)

// whoisRegistryDefault is one curated registry: the suffix, its own WHOIS
// server, how to read the expiration out of its answer and how its "free"
// answer looks.
//
// The table is the single source of truth: DefaultWhoisParsers turns it into
// the rows of whois_parsers, and WhoisServerFor answers "which server owns this
// TLD" without asking whois.iana.org.
type whoisRegistryDefault struct {
	// TLD is the suffix without a leading dot.
	TLD string
	// Server is the registry's own port 43 server (never whois.iana.org).
	Server string
	// ExpiryRegex captures the date text.
	ExpiryRegex string
	// DateLayouts is the ";" separated list of Go layouts for that text.
	DateLayouts string
	// NotFoundPattern overrides the shared default when the registry has its
	// own wording for "the domain is free".
	NotFoundPattern string
	// Note is the short explanation shown in the admin table.
	Note string
}

// whoisBuiltinNote marks every row the seed installed.
const whoisBuiltinNote = "built-in default"

// whoisRegistryDefaults lists the TLDs without RDAP whose registry publishes
// the expiration date over WHOIS.
//
// Every entry was verified against the live registry (the probe recorded the
// exact line each one answers with, and whois_defaults_live_test.go re-checks
// the table on demand). Registries that hide the date are absent on purpose: a
// rule that can never match would only turn their monitors into errors, which
// is worse than the honest "unsupported".
//
// Grouped by the shape of the answer and ordered by how much a rule matters,
// the most used suffixes first.
var whoisRegistryDefaults = []whoisRegistryDefault{
	// --- ISO 8601 timestamps (the IANA style: "Registry Expiry Date") ---
	{TLD: "io", Server: "whois.nic.io", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "us", Server: "whois.nic.us", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "me", Server: "whois.nic.me", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "ie", Server: "whois.weare.ie", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "my", Server: "whois.mynic.my", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "ru", Server: "whois.tcinet.ru", ExpiryRegex: `(?im)^\s*paid-till:\s*(\S+)`, DateLayouts: layoutISO8601},
	{TLD: "hr", Server: "whois.dns.hr", ExpiryRegex: `(?im)^\s*Registrar Registration Expiration Date:\s*(\S+)`, DateLayouts: layoutISO8601},
	{TLD: "ug", Server: "whois.co.ug", ExpiryRegex: `(?im)^\s*(?:Privacy Expiry|Expires On):\s*(\S+)`, DateLayouts: layoutISO8601},
	{TLD: "ac", Server: "whois.nic.ac", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "bf", Server: "whois.registre.bf", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "bh", Server: "whois.nic.bh", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "bi", Server: "whois1.nic.bi", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "bj", Server: "whois.nic.bj", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "co", Server: "whois.registry.co", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "gi", Server: "whois.identitydigital.services", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "gl", Server: "whois.nic.gl", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "la", Server: "whois.nic.la", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "sh", Server: "whois.nic.sh", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "su", Server: "whois.tcinet.ru", ExpiryRegex: `(?im)^\s*paid-till:\s*(\S+)`, DateLayouts: layoutISO8601},
	{TLD: "sx", Server: "whois.sx", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "vc", Server: "whois.identitydigital.services", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},

	// --- the same ISO shape, less common suffixes ---
	{TLD: "af", Server: "whois.nic.af", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "ag", Server: "whois.nic.ag", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "ci", Server: "whois.nic.ci", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "dm", Server: "whois.dmdomains.dm", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "do", Server: "whois.nic.do", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "gh", Server: "whois.nic.gh", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601Millis, Note: "ISO 8601 with milliseconds"},
	{TLD: "gn", Server: "whois.ande.gov.gn", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "ki", Server: "whois.nic.ki", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "kn", Server: "whois.nic.kn", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "mn", Server: "whois.nic.mn", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "mr", Server: "whois.nic.mr", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "mz", Server: "whois.nic.mz", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "pr", Server: "whois.afilias-srs.net", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "sb", Server: "whois.nic.net.sb", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "sc", Server: "whois.nic.sc", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "so", Server: "whois.nic.so", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "td", Server: "whois.nic.td", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},
	{TLD: "tl", Server: "whois.nic.tl", ExpiryRegex: whoisRegexRegistryExpiry, DateLayouts: layoutISO8601},

	// --- date plus time (non ISO separators) ---
	{TLD: "cn", Server: "whois.cnnic.cn", ExpiryRegex: `(?im)^\s*Expiration Time:\s*(\S+ \S+)`, DateLayouts: layoutDateTime},
	{TLD: "cl", Server: "whois.nic.cl", ExpiryRegex: `(?im)^\s*Expiration date:\s*(\S+ \S+ \S+)`, DateLayouts: layoutDateTimeZone},
	{TLD: "pt", Server: "whois.dns.pt", ExpiryRegex: `(?im)^\s*Expiration Date:\s*(\S+ \S+)`, DateLayouts: layoutSlashDateTime},
	{TLD: "rs", Server: "whois.rnids.rs", ExpiryRegex: `(?im)^\s*Expiration date:\s*(\S+ \S+)`, DateLayouts: layoutDotDateTime},
	{TLD: "im", Server: "whois.nic.im", ExpiryRegex: `(?im)^\s*Expiry Date:\s*(\S+ \S+)`, DateLayouts: layoutSlashDateTime},

	// --- short month names ---
	{TLD: "tr", Server: "whois.trabis.gov.tr", ExpiryRegex: `(?im)^\s*Expires on\.*:\s*([^.]+)`, DateLayouts: layoutYearMonthShort},
	{TLD: "edu", Server: "whois.educause.edu", ExpiryRegex: `(?im)^\s*Domain expires:\s*(\S+)`, DateLayouts: layoutDayMonthShort},
	{TLD: "bn", Server: "whois.bnnic.bn", ExpiryRegex: `(?im)^\s*Expiration Date:\s*(\S+ \S+)`, DateLayouts: layoutDayMonthShortDateTime},

	// --- day first, numeric separators ---
	{TLD: "hk", Server: "whois.hkirc.hk", ExpiryRegex: `(?im)^\s*Expiry Date:\s*(\S+)`, DateLayouts: layoutDayMonthYear},
	{TLD: "ax", Server: "whois.ax", ExpiryRegex: `(?im)^\s*expires\.*:\s*(\S+)`, DateLayouts: layoutDotDate},
	{TLD: "ls", Server: "whois.nic.ls", ExpiryRegex: `(?im)^\s*expire:\s*(\S+)`, DateLayouts: layoutDotDate},
	{TLD: "mk", Server: "whois.marnet.mk", ExpiryRegex: `(?im)^\s*expire:\s*(\S+)`, DateLayouts: layoutDotDate},
	{TLD: "ve", Server: "whois.nic.ve", ExpiryRegex: `(?im)^\s*expire:\s*(\S+)`, DateLayouts: layoutDotDate},

	// --- plain calendar dates ---
	{TLD: "it", Server: "whois.nic.it", ExpiryRegex: whoisRegexExpireDate, DateLayouts: layoutDate},
	{TLD: "se", Server: "whois.iis.se", ExpiryRegex: whoisRegexExpires, DateLayouts: layoutDate},
	{TLD: "dk", Server: "whois.punktum.dk", ExpiryRegex: whoisRegexExpires, DateLayouts: layoutDate},
	{TLD: "lt", Server: "whois.domreg.lt", ExpiryRegex: whoisRegexExpires, DateLayouts: layoutDate},
	{TLD: "mx", Server: "whois.mx", ExpiryRegex: whoisRegexExpirationDate, DateLayouts: layoutDate},
	{TLD: "by", Server: "whois.cctld.by", ExpiryRegex: whoisRegexExpirationDate, DateLayouts: layoutDate},
	{TLD: "am", Server: "whois.amnic.net", ExpiryRegex: whoisRegexExpires, DateLayouts: layoutDate},
	{TLD: "pk", Server: "whois.pknic.net.pk", ExpiryRegex: `(?im)^\s*Expiry Date:\s*(\S+)`, DateLayouts: layoutDate},
	{TLD: "ee", Server: "whois.tld.ee", ExpiryRegex: `(?im)^\s*expire:\s*(\S+)`, DateLayouts: layoutDate},
	{TLD: "mc", Server: "whois.nic.mc", ExpiryRegex: `(?im)^\s*Expires on\s*:\s*(\S+)`, DateLayouts: layoutDate},
	{TLD: "st", Server: "whois.nic.st", ExpiryRegex: whoisRegexExpirationDate, DateLayouts: layoutDate},
	{TLD: "tg", Server: "whois.nic.tg", ExpiryRegex: `(?im)^\s*Expiration:\.*\s*(\S+)`, DateLayouts: layoutDate},
}

// DefaultWhoisParsers returns a fresh copy of the built-in rules (a caller may
// normalize, edit or persist them without touching the table).
func DefaultWhoisParsers() []WhoisParser {
	parsers := make([]WhoisParser, 0, len(whoisRegistryDefaults))
	for _, entry := range whoisRegistryDefaults {
		note := entry.Note
		if note == "" {
			note = whoisBuiltinNote
		}
		pattern := entry.NotFoundPattern
		if pattern == "" {
			pattern = DefaultWhoisNotFoundPattern
		}
		parser := WhoisParser{
			TLD:             entry.TLD,
			Server:          entry.Server,
			ExpiryRegex:     entry.ExpiryRegex,
			DateLayouts:     entry.DateLayouts,
			NotFoundPattern: pattern,
			Note:            note,
			Enabled:         true,
		}
		parser.Normalize()
		parsers = append(parsers, parser)
	}
	return parsers
}

// WhoisServerFor returns the registry WHOIS server of a TLD when the built-in
// table knows it.
//
// It accepts a plain suffix ("io"), a dotted one (".io") or a whole domain
// ("example.io"); only the last label is considered, because a port 43 server
// belongs to a TLD. The lookup exists so a query never has to ask
// whois.iana.org for something we already know.
func WhoisServerFor(tld string) (string, bool) {
	value := strings.ToLower(strings.Trim(strings.TrimSpace(tld), "."))
	if value == "" {
		return "", false
	}
	if index := strings.LastIndex(value, "."); index >= 0 {
		value = value[index+1:]
	}
	if value == "" {
		return "", false
	}
	for _, entry := range whoisRegistryDefaults {
		if entry.TLD == value {
			return entry.Server, true
		}
	}
	return "", false
}
