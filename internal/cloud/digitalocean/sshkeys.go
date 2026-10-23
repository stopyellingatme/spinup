package digitalocean

import (
	"context"
	"crypto/md5" //nolint:gosec // DigitalOcean shows MD5 fingerprints.
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SSHKey is an account SSH key.
type SSHKey struct {
	ID          int    `json:"id"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
	Name        string `json:"name"`
}

// Fingerprint returns the MD5 fingerprint of an OpenSSH public key line, as
// DigitalOcean shows it: lower-case hex pairs with colons.
func Fingerprint(pub string) (string, error) {
	f := strings.Fields(pub)
	if len(f) < 2 {
		return "", errors.New("cannot read the SSH public key: it needs a type and a base64 blob\nTry: pass a public key file such as ~/.ssh/id_ed25519.pub")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) == 0 {
		return "", errors.New("cannot read the SSH public key: the blob is not valid base64\nTry: pass a public key file such as ~/.ssh/id_ed25519.pub")
	}
	sum := md5.Sum(blob) //nolint:gosec // see the import.
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":"), nil
}

// SSHKeyByFingerprint reads one key. It returns nil, nil when the key does
// not exist. It never uses the list endpoint, which allows 10 requests a minute.
func (c *Client) SSHKeyByFingerprint(ctx context.Context, fp string) (*SSHKey, error) {
	var out struct {
		SSHKey SSHKey `json:"ssh_key"`
	}
	err := c.do(ctx, http.MethodGet, "/v2/account/keys/"+url.PathEscape(fp), nil, &out)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out.SSHKey, nil
}

// EnsureSSHKey makes sure the account holds the key. It reports whether it
// created the key.
func (c *Client) EnsureSSHKey(ctx context.Context, name, pub string) (SSHKey, bool, error) {
	fp, err := Fingerprint(pub)
	if err != nil {
		return SSHKey{}, false, err
	}
	k, err := c.SSHKeyByFingerprint(ctx, fp)
	if err != nil {
		return SSHKey{}, false, err
	}
	if k != nil {
		return *k, false, nil
	}
	var out struct {
		SSHKey SSHKey `json:"ssh_key"`
	}
	body := map[string]string{"name": name, "public_key": strings.TrimSpace(pub)}
	err = c.do(ctx, http.MethodPost, "/v2/account/keys", body, &out)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity {
		// Another run may have added the key. Look it up again.
		k, lerr := c.SSHKeyByFingerprint(ctx, fp)
		if lerr != nil {
			return SSHKey{}, false, lerr
		}
		if k != nil {
			return *k, false, nil
		}
	}
	if err != nil {
		return SSHKey{}, false, err
	}
	return out.SSHKey, true, nil
}
