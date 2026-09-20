package ical

import "bytes"

// Microsoft Exchange (and its ICS feeds) emit Windows-style timezone names
// like "Eastern Standard Time" in DTSTART;TZID= parameters. Go's time
// package only knows IANA names ("America/New_York"), so arran4/golang-ical
// silently drops every event with such a TZID.
//
// We fix this before parsing: a small map translates the Windows names that
// actually appear in Outlook feeds to their IANA equivalents, and a byte-
// level rewrite swaps them in place. The (now orphan) VTIMEZONE block is
// harmless — Go's tzdata resolves the IANA name directly.
//
// The mapping is a subset of Microsoft's CLDR windowsZones.xml — the ones
// we have seen in real feeds plus a broad set for robustness. Names not in
// the map are left alone; the event will still be dropped, but only for
// truly obscure zones.

var windowsToIANA = map[string]string{
	// Africa
	"Egypt Standard Time":         "Africa/Cairo",
	"Morocco Standard Time":       "Africa/Casablanca",
	"South Africa Standard Time":  "Africa/Johannesburg",
	"W. Central Africa Standard Time": "Africa/Lagos",
	// Americas
	"Alaskan Standard Time":       "America/Anchorage",
	"Argentina Standard Time":     "America/Buenos_Aires",
	"Atlantic Standard Time":      "America/Halifax",
	"Canada Central Standard Time": "America/Regina",
	"Central America Standard Time": "America/Guatemala",
	"Central Brazilian Standard Time": "America/Cuiaba",
	"Central Standard Time":       "America/Chicago",
	"Central Standard Time (Mexico)": "America/Mexico_City",
	"Eastern Standard Time":       "America/New_York",
	"Eastern Standard Time (Mexico)": "America/Cancun",
	"E. South America Standard Time": "America/Sao_Paulo",
	"Greenland Standard Time":     "America/Godthab",
	"Hawaiian Standard Time":      "Pacific/Honolulu",
	"Mountain Standard Time":      "America/Denver",
	"Mountain Standard Time (Mexico)": "America/Chihuahua",
	"Newfoundland Standard Time":  "America/St_Johns",
	"Pacific SA Standard Time":    "America/Santiago",
	"Pacific Standard Time":       "America/Los_Angeles",
	"Pacific Standard Time (Mexico)": "America/Tijuana",
	"Paraguay Standard Time":      "America/Asuncion",
	"SA Eastern Standard Time":    "America/Cayenne",
	"SA Pacific Standard Time":    "America/Bogota",
	"SA Western Standard Time":    "America/La_Paz",
	"US Eastern Standard Time":    "America/Indianapolis",
	"US Mountain Standard Time":   "America/Phoenix",
	"Venezuela Standard Time":     "America/Caracas",
	// Asia
	"Arab Standard Time":          "Asia/Riyadh",
	"Arabian Standard Time":       "Asia/Dubai",
	"Arabic Standard Time":        "Asia/Baghdad",
	"Bangladesh Standard Time":    "Asia/Dhaka",
	"Central Asia Standard Time":  "Asia/Almaty",
	"China Standard Time":         "Asia/Shanghai",
	"Georgian Standard Time":      "Asia/Tbilisi",
	"India Standard Time":         "Asia/Kolkata",
	"Iran Standard Time":          "Asia/Tehran",
	"Israel Standard Time":        "Asia/Jerusalem",
	"Jordan Standard Time":        "Asia/Amman",
	"Korea Standard Time":         "Asia/Seoul",
	"Middle East Standard Time":   "Asia/Beirut",
	"Nepal Standard Time":         "Asia/Kathmandu",
	"North Asia East Standard Time": "Asia/Irkutsk",
	"North Asia Standard Time":    "Asia/Krasnoyarsk",
	"Pakistan Standard Time":      "Asia/Karachi",
	"SE Asia Standard Time":       "Asia/Bangkok",
	"Singapore Standard Time":     "Asia/Singapore",
	"Sri Lanka Standard Time":     "Asia/Colombo",
	"Syria Standard Time":         "Asia/Damascus",
	"Taipei Standard Time":        "Asia/Taipei",
	"Tokyo Standard Time":         "Asia/Tokyo",
	"Turkey Standard Time":        "Europe/Istanbul",
	"Ulaanbaatar Standard Time":   "Asia/Ulaanbaatar",
	"Vladivostok Standard Time":   "Asia/Vladivostok",
	"West Asia Standard Time":     "Asia/Tashkent",
	"West Pacific Standard Time":  "Pacific/Port_Moresby",
	"Yakutsk Standard Time":       "Asia/Yakutsk",
	// Europe
	"Central European Standard Time": "Europe/Warsaw",
	"Central Europe Standard Time":   "Europe/Budapest",
	"E. Europe Standard Time":     "Europe/Chisinau",
	"FLE Standard Time":           "Europe/Helsinki",
	"GTB Standard Time":           "Europe/Bucharest",
	"GMT Standard Time":           "Europe/London",
	"Greenwich Standard Time":     "Atlantic/Reykjavik",
	"Kaliningrad Standard Time":   "Europe/Kaliningrad",
	"Romance Standard Time":       "Europe/Paris",
	"Russian Standard Time":       "Europe/Moscow",
	"W. Europe Standard Time":     "Europe/Berlin",
	// Oceania
	"AUS Central Standard Time":   "Australia/Darwin",
	"AUS Eastern Standard Time":   "Australia/Sydney",
	"Cen. Australia Standard Time": "Australia/Adelaide",
	"E. Australia Standard Time":  "Australia/Brisbane",
	"Fiji Standard Time":          "Pacific/Fiji",
	"New Zealand Standard Time":   "Pacific/Auckland",
	"Samoa Standard Time":         "Pacific/Apia",
	"Tasmania Standard Time":      "Australia/Hobart",
	"W. Australia Standard Time":  "Australia/Perth",
	// UTC / GMT literals — sometimes appear as VTIMEZONE ids
	"UTC":                       "UTC",
	"Coordinated Universal Time": "UTC",
}

// rewriteWindowsTZIDs replaces Windows-style TZID names with their IANA
// equivalents in-place in an ICS document. Idempotent — running it twice
// is a no-op. Names that are not in the map are left alone.
func rewriteWindowsTZIDs(body []byte) []byte {
	if !bytes.Contains(body, []byte("TZID")) {
		return body
	}
	for winName, ianaName := range windowsToIANA {
		if winName == ianaName {
			continue
		}
		// Property parameter form: DTSTART;TZID=Eastern Standard Time:20260920T100000
		// The name is bounded by ':' (start of the value).
		paramFrom := []byte("TZID=" + winName + ":")
		paramTo := []byte("TZID=" + ianaName + ":")
		body = bytes.ReplaceAll(body, paramFrom, paramTo)

		// VTIMEZONE header form: TZID:Eastern Standard Time\r\n or \n.
		// Rewriting these keeps the (now orphan) VTIMEZONE block consistent
		// even though Go's tzdata resolves the IANA name directly.
		crlfFrom := []byte("TZID:" + winName + "\r\n")
		crlfTo := []byte("TZID:" + ianaName + "\r\n")
		body = bytes.ReplaceAll(body, crlfFrom, crlfTo)

		lfFrom := []byte("TZID:" + winName + "\n")
		lfTo := []byte("TZID:" + ianaName + "\n")
		body = bytes.ReplaceAll(body, lfFrom, lfTo)
	}
	return body
}
