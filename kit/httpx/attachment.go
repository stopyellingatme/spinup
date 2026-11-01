package httpx

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxFilenameBytes limits the file name of an attachment.
const maxFilenameBytes = 200

// WriteAttachment writes body as a download (ADR 0026 decision 6.1). It sets
// Content-Type, Content-Length, Cache-Control: private, no-store,
// X-Content-Type-Options: nosniff, and Content-Disposition: attachment with an
// ASCII filename and an RFC 5987 filename*. It removes quotes, slashes, control
// characters, and line breaks from filename. An empty result becomes "download".
// A contentType that is not a valid media type becomes application/octet-stream.
func WriteAttachment(w http.ResponseWriter, filename, contentType string, body []byte) {
	if _, _, err := mime.ParseMediaType(contentType); err != nil || strings.ContainsAny(contentType, "\r\n") {
		contentType = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", contentDisposition(filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// contentDisposition returns the Content-Disposition value for filename:
//
//	attachment; filename="<ASCII>"; filename*=UTF-8''<percent-encoded>
func contentDisposition(filename string) string {
	name := cleanFilename(filename)
	return `attachment; filename="` + asciiFilename(name) + `"; filename*=UTF-8''` + encodeRFC5987(name)
}

// cleanFilename removes quotes, slashes, backslashes, control characters, format
// characters (such as a right-to-left override), and line or paragraph
// separators. It trims spaces and dots at the ends, limits the length, and
// returns "download" for an empty result.
func cleanFilename(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '"' || r == '\'' || r == '/' || r == '\\':
		case unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp):
		case r == utf8.RuneError:
		default:
			if b.Len()+utf8.RuneLen(r) > maxFilenameBytes {
				return finishFilename(b.String())
			}
			b.WriteRune(r)
		}
	}
	return finishFilename(b.String())
}

func finishFilename(s string) string {
	s = strings.Trim(s, " .")
	if s == "" {
		return "download"
	}
	return s
}

// asciiFilename replaces each byte that is not printable ASCII, and the
// characters that some browsers decode or split on (%, ;, and ,), with '_'.
func asciiFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '%' || r == ';' || r == ',' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// encodeRFC5987 percent-encodes s as a value of filename* (RFC 5987 attr-char).
func encodeRFC5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

func isAttrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
