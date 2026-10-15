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
