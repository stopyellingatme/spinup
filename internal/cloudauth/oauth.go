package cloudauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBody = 1 << 20

type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	TokenType    string          `json:"token_type"`
	Scope        string          `json:"scope"`
	ExpiresIn    int64           `json:"expires_in"`
	Info         json.RawMessage `json:"info"`
	Error        string          `json:"error"`
	Description  string          `json:"error_description"`
}

func post(ctx context.Context, cfg Config, endpoint string, form url.Values, bearer string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, problem("call the sign-in server", "the endpoint URL is not valid", "check the OAuth URLs in the configuration")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := cfg.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return 0, nil, problem("call the sign-in server", err.Error(), "check the network connection, then try again")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, problem("read the sign-in answer", err.Error(), "try again")
	}
	return resp.StatusCode, body, nil
}

// serverError turns a failed answer into an error. It never includes a token.
func serverError(what string, status int, body []byte) error {
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr)
	if tr.Error == "invalid_grant" {
		return fmt.Errorf("%s: %w (invalid_grant)\nTry: sign in again", what, ErrReauth)
	}
	why := fmt.Sprintf("the server answered HTTP %d", status)
	if tr.Error != "" {
		why += ": " + tr.Error
		if tr.Description != "" {
			why += " (" + tr.Description + ")"
		}
	}
	return problem(what, why, "sign in again, or use a personal access token")
}

func tokenCall(ctx context.Context, cfg Config, what string, form url.Values) (Token, error) {
	status, body, err := post(ctx, cfg, cfg.TokenURL, form, "")
	if err != nil {
		return Token{}, err
	}
	if status != http.StatusOK {
		return Token{}, serverError(what, status, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return Token{}, problem(what, "the server answer has no access token", "sign in again")
	}
	t := Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		Scope:        tr.Scope,
		ClientID:     cfg.ClientID,
		Info:         tr.Info,
	}
	// expires_in 0 or missing means the expiry is unknown. Expiry stays the zero time.
	if tr.ExpiresIn > 0 {
		t.Expiry = cfg.now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

// Exchange trades an authorization code for a token. The request carries no client secret.
func Exchange(ctx context.Context, cfg Config, code, verifier, redirectURI string) (Token, error) {
	return tokenCall(ctx, cfg, "exchange the sign-in code", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	})
}

// Refresh trades a refresh token for a new token. If the answer has no new refresh token,
// the result keeps the old one. Refresh tokens can be single-use, so the caller must save the result.
func Refresh(ctx context.Context, cfg Config, refreshToken string) (Token, error) {
	t, err := tokenCall(ctx, cfg, "refresh the sign-in", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {cfg.ClientID},
	})
	if err == nil && t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, err
}

// Revoke revokes a token. The server answers 200 or 204.
func Revoke(ctx context.Context, cfg Config, token string) error {
	if cfg.RevokeURL == "" {
		return problem("revoke the token", "no revoke URL is configured", "")
	}
	status, body, err := post(ctx, cfg, cfg.RevokeURL, url.Values{"token": {token}}, token)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return serverError("revoke the token", status, body)
	}
	return nil
}
