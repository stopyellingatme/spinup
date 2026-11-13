package httpx

import (
	"strconv"
	"strings"
)

// AcceptsEncoding reports whether header permits coding.
func AcceptsEncoding(header, coding string) bool {
	if header == "" || coding == "" {
		return false
	}

	wildcard := false
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
		wildcard = quality > 0
	}
	return specified || wildcard
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
