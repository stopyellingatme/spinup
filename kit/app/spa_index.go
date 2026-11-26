package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"golang.org/x/net/html"
)

type spaFile struct {
	br, gzip     spaVariant
	etag         string
	contentType  string
	cacheControl string
}

type spaVariant struct {
	name string
	etag string
}

func indexSPA(files fs.FS, immutable string) (map[string]spaFile, string) {
	csp := spaCSP(nil)
	if files == nil {
		return nil, csp
	}
	index := make(map[string]struct{})
	var names []string
	// WalkDir visits names in lexical order. Keep siblings out of the public index.
	_ = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			index[name] = struct{}{}
			names = append(names, name)
		}
		return nil
	})
	public := make(map[string]spaFile, len(index))
	for _, name := range names {
		if ext := path.Ext(name); ext == ".br" || ext == ".gz" {
			if _, sibling := index[strings.TrimSuffix(name, ext)]; sibling {
				continue
			}
		}
		file := spaFile{contentType: mime.TypeByExtension(path.Ext(name))}
		br, gzip := name+".br", name+".gz"
		if _, ok := index[br]; ok {
			file.br.name = br
		}
		if _, ok := index[gzip]; ok {
			file.gzip.name = gzip
		}
		isImmutable := strings.HasPrefix(name, immutable)
		file.cacheControl = "public, max-age=31536000, immutable"
		if !isImmutable {
			file.cacheControl = "no-cache"
			data, err := fs.ReadFile(files, name)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(data)
			tag := hex.EncodeToString(sum[:16])
			file.etag = "\"" + tag + "\""
			if file.br.name != "" {
				file.br.etag = "\"" + tag + ".br\""
			}
			if file.gzip.name != "" {
				file.gzip.etag = "\"" + tag + ".gz\""
			}
			if name == "index.html" {
				csp = spaCSP(data)
			}
			if file.contentType == "" {
				file.contentType = http.DetectContentType(data)
			}
		} else if file.contentType == "" {
			// Unknown extensions need a plain sample, even for an encoded response.
			f, err := files.Open(name)
			if err != nil {
				continue
			}
			var sample [512]byte
			n, err := io.ReadFull(f, sample[:])
			_ = f.Close()
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				continue
			}
			file.contentType = http.DetectContentType(sample[:n])
		}
		public[name] = file
	}
	return public, csp
}

// spaCSP gives the policy of the shell. The directives equal the node-mode policy of the web
// pack, with script hashes in place of the nonce. form-action has no default-src fallback, so the
// policy names it.
func spaCSP(index []byte) string {
	var policy strings.Builder
	policy.WriteString("default-src 'self'; script-src 'self'")
	tokens := html.NewTokenizer(bytes.NewReader(index))
	var body []byte
	inline := false
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			policy.WriteString("; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
			return policy.String()
		case html.StartTagToken:
			token := tokens.Token()
			if token.Data != "script" {
				continue
			}
			inline = true
			body = body[:0]
			for _, attr := range token.Attr {
				if attr.Key == "src" {
					inline = false
				}
			}
		case html.TextToken:
			if inline {
				// A browser hashes the script text after it normalizes CRLF and CR to LF.
				// Text does the same and does not unescape the raw text of a script.
				body = append(body, tokens.Text()...)
			}
		case html.EndTagToken:
			if token := tokens.Token(); inline && token.Data == "script" {
				sum := sha256.Sum256(body)
				policy.WriteString(" 'sha256-")
				policy.WriteString(base64.StdEncoding.EncodeToString(sum[:]))
				policy.WriteByte('\'')
				inline = false
			}
		}
	}
}
