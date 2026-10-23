package templates_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// TestWebResponseSecurity checks response headers for anonymous and authenticated pages.
func TestWebResponseSecurity(t *testing.T) {
	t.Parallel()
	data, err := fs.ReadFile(templates.Embedded(), "web/files/web/src/hooks.server.ts")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ReplaceAll(string(data), "import { API_URL } from '$app/env/private';", "const API_URL = 'http://api.test';\nconst __PUBLIC_ORIGIN__ = 'http://web.test';")
	source += `
import assert from 'node:assert/strict';
for (const user of [null, { id: 1, email: 'user@example.com' }]) {
 for (const path of ['/', '/login', '/register', '/account', '/logout']) {
  for (const custom of [false, true]) {
   globalThis.fetch = async () => new Response(JSON.stringify({ user }));
   const headers = custom ? { 'X-Frame-Options': 'SAMEORIGIN', 'Content-Security-Policy': "default-src 'self'", 'Cache-Control': 'public' } : {};
   const response = await handle({
    event: { locals: {}, url: new URL(path, 'http://web.test'), request: new Request('http://web.test', { headers: { cookie: 'session=test' } }) },
    resolve: async () => new Response('page', { headers })
   });
   assert.equal(response.headers.get('X-Frame-Options'), custom ? 'SAMEORIGIN' : 'DENY', path);
   assert.equal(response.headers.get('Content-Security-Policy'), custom ? "default-src 'self'" : "frame-ancestors 'none'", path);
   assert.equal(response.headers.get('Cache-Control'), user ? 'no-store' : custom ? 'public' : null, path);
  }
 }
}
`
	path := filepath.Join(t.TempDir(), "hooks.mts")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := run.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (run.OSRunner{}).Run(context.Background(), run.Cmd{Name: node, Args: []string{path}})
	if err != nil {
		t.Fatalf("response security: %v\n%s", err, result.Stderr)
	}
}

// TestWebHandleFetch checks that handleFetch passes the client address to the API only when
// ADDRESS_HEADER is set, and never passes a header from the browser.
func TestWebHandleFetch(t *testing.T) {
	t.Parallel()
	data, err := fs.ReadFile(templates.Embedded(), "web/files/web/src/hooks.server.ts")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ReplaceAll(string(data), "import { API_URL } from '$app/env/private';", "const API_URL = 'http://api.test';\nconst __PUBLIC_ORIGIN__ = 'http://web.test';")
	source += `
import assert from 'node:assert/strict';
const forwarding = Boolean(process.env.ADDRESS_HEADER);
const warnings = [];
console.warn = (...args) => warnings.push(args);

// call runs handleFetch for one request and returns the request that reaches the API.
async function call(url, address) {
 let seen;
 const event = {
  request: new Request('http://web.test/login', { headers: { cookie: 'session=test' } }),
  getClientAddress: () => {
   if (address instanceof Error) throw address;
   return address;
  }
 };
 const request = new Request(url, { method: 'POST', body: '{}', headers: { 'x-forwarded-for': '10.9.9.9', cookie: 'session=forged' } });
 await handleFetch({ event, request, fetch: async (req) => { seen = req; return new Response('ok'); } });
 return seen;
}

const sent = await call('http://web.test/api/auth/login', '198.51.100.7');
assert.equal(sent.url, 'http://api.test/api/auth/login');
assert.equal(sent.headers.get('cookie'), 'session=test');
assert.equal(sent.headers.get('x-forwarded-for'), forwarding ? '198.51.100.7' : null);
assert.equal(warnings.length, 0);

// getClientAddress throws when the proxy header is absent: send no header, warn once.
for (let i = 0; i < 3; i++) {
 const missing = await call('http://web.test/api/auth/login', new Error('no header'));
 assert.equal(missing.headers.get('x-forwarded-for'), null);
}
assert.equal(warnings.length, forwarding ? 1 : 0);

// A request to another origin keeps its own headers.
const other = await call('http://other.test/data', '198.51.100.7');
assert.equal(other.url, 'http://other.test/data');
assert.equal(other.headers.get('x-forwarded-for'), '10.9.9.9');
`
	path := filepath.Join(t.TempDir(), "hooks.mts")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := run.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"address header set", []string{"ADDRESS_HEADER=X-Forwarded-For"}},
		{"address header unset", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := (run.OSRunner{}).Run(context.Background(), run.Cmd{Name: node, Args: []string{path}, Env: tc.env, ReplaceEnv: true})
			if err != nil {
				t.Fatalf("handleFetch: %v\n%s", err, result.Stderr)
			}
		})
	}
}
