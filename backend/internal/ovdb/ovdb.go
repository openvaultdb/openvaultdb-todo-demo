// Package ovdb is a thin server-side client for the OpenVaultDB local server
// HTTP API (see openvaultdb-com/interface/main.tsp). It implements the connect
// flow (authorize redirect + token exchange) and record CRUD against a granted
// namespace, all authenticated with a scoped app bearer token.
package ovdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fixed integration constants from openvaultdb-com/interface/INTEGRATION.md.
// These MUST match the OVDB server and the manifest verbatim. The vault server's
// base URL and the vault id are NOT pinned here — they are chosen by the user in
// OVDB Connect and learned at runtime (the `iss` callback param and the token
// response's `vault` field), then carried in a Conn.
const (
	NamespaceID = "todo-demo.openvaultdb.app/openvaultdb/todos"
	Collection  = "tasks"
	ClientID    = "todo-demo.openvaultdb.app"
	RedirectURI = "http://localhost:5180/callback"
	Role        = "editor"
	FrontendURL = "http://localhost:5173"
)

// ConnectURL builds the OVDB Connect URL the backend redirects the user to at
// the start of the connect flow. Connect (openvaultdb.com) lets the user pick a
// registered vault or paste a connection string, then forwards to that vault
// server's /authorize. Note: no `vault` or server base URL — Connect resolves it.
func ConnectURL(connectBase, state string) string {
	q := url.Values{}
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", RedirectURI)
	q.Set("namespaceId", NamespaceID)
	q.Set("role", Role)
	q.Set("state", state)
	return strings.TrimRight(connectBase, "/") + "?" + q.Encode()
}

// Conn is a live, scoped connection to one vault on one OVDB server, established
// by the connect flow. BaseURL comes from the `iss` callback param; VaultID and
// Token come from the token exchange.
type Conn struct {
	BaseURL string
	VaultID string
	Token   string
}

// TokenResponse is the scoped app token returned by POST /token.
type TokenResponse struct {
	AccessToken string              `json:"access_token"`
	TokenType   string              `json:"token_type"`
	ExpiresIn   int32               `json:"expires_in"`
	NamespaceID string              `json:"namespaceId"`
	Scope       map[string][]string `json:"scope"`
	Vault       string              `json:"vault"`
}

// Client talks to the OVDB server. The zero value is not usable; use New.
type Client struct {
	http *http.Client
}

// New returns a Client with a sane timeout.
func New() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

// ExchangeCode swaps an authorization code for a scoped app token via POST
// {baseURL}/token, where baseURL is the issuing vault server (from `iss`).
func (c *Client) ExchangeCode(ctx context.Context, baseURL, code string) (*TokenResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"client_id":    ClientID,
		"redirect_uri": RedirectURI,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	var tok TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// recordsPath builds the record-collection path on the connection's vault
// server, with the namespace id URL-encoded (its "/" become "%2F"), per
// INTEGRATION.md.
func recordsPath(conn Conn, id string) string {
	encNS := url.PathEscape(NamespaceID)
	p := fmt.Sprintf("%s/vaults/%s/ns/%s/collections/%s/records",
		strings.TrimRight(conn.BaseURL, "/"), url.PathEscape(conn.VaultID), encNS, Collection)
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}

// Record is an opaque OVDB record; shape is governed by the collection schema.
type Record map[string]any

func (c *Client) do(ctx context.Context, method, urlStr, token string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// ListRecords returns all records in the tasks collection.
func (c *Client) ListRecords(ctx context.Context, conn Conn) ([]Record, error) {
	resp, err := c.do(ctx, http.MethodGet, recordsPath(conn, ""), conn.Token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	var recs []Record
	if err := json.NewDecoder(resp.Body).Decode(&recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// CreateRecord creates a record and returns the server-materialized record.
func (c *Client) CreateRecord(ctx context.Context, conn Conn, rec Record) (Record, error) {
	resp, err := c.do(ctx, http.MethodPost, recordsPath(conn, ""), conn.Token, rec)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, apiError(resp)
	}
	var out Record
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateRecord patches a record by id and returns the updated record.
func (c *Client) UpdateRecord(ctx context.Context, conn Conn, id string, patch Record) (Record, error) {
	resp, err := c.do(ctx, http.MethodPatch, recordsPath(conn, id), conn.Token, patch)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	var out Record
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteRecord removes a record by id.
func (c *Client) DeleteRecord(ctx context.Context, conn Conn, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, recordsPath(conn, id), conn.Token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return apiError(resp)
	}
	return nil
}

// apiError reads an OVDB ApiError body (or falls back to the status line).
func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		return fmt.Errorf("ovdb %d: %s: %s", resp.StatusCode, e.Code, e.Message)
	}
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("ovdb %d: %s", resp.StatusCode, msg)
}
