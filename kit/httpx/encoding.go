package httpx

import (
	"strconv"
	"strings"
)

// AcceptsEncoding reports whether the Accept-Encoding value header permits coding.
// A named coding wins over the wildcard. The coding identity is permitted unless
// the header refuses it by name or refuses the wildcard without naming it (RFC 9110 §12.5.3).
func AcceptsEncoding(header, coding string) bool {
	if coding == "" {
		return false
	}
	identity := strings.EqualFold(coding, "identity")
	if header == "" {
		return identity
	}

	wildcard, wildcardSeen := false, false
	specified := false
	for value := range strings.SplitSeq(header, ",") {
		parts := strings.Split(value, ";")
		name := strings.TrimSpace(parts[0])
		if !strings.EqualFold(name, coding) && name != "*" {
			continue
		}

		quality := encodingQuality(parts[1:])
		if strings.EqualFold(name, coding) {
			specified = true
			if quality == 0 {
				return false
			}
			continue
		}
		wildcard, wildcardSeen = quality > 0, true
	}
	if specified {
		return true
	}
	if wildcardSeen {
		return wildcard
	}
	return identity
}

func encodingQuality(parameters []string) float64 {
	quality := 1.0
	for _, parameter := range parameters {
		key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
		if !ok || !strings.EqualFold(key, "q") {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed != parsed || parsed < 0 || parsed > 1 {
			return 0
		}
		quality = parsed
	}
	return quality
}
