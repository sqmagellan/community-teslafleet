package onboard

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func itoa(n int) string { return strconv.Itoa(n) }

// postEnroll sends a fleet_telemetry_config to the vehicle-command proxy (which signs
// it with the partner key). The proxy uses a self-signed cert on the internal hop.
func postEnroll(proxyURL, accessToken string, payload []byte) error {
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	url := strings.TrimRight(proxyURL, "/") + "/api/1/vehicles/fleet_telemetry_config"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

// Tesla OAuth scopes the gateway needs (device data + commands + charging).
const oauthScopes = "openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds"

// teslaClient performs the onboarding API calls against Tesla's auth server and the
// regional Fleet API. NOTE: end-to-end behaviour is only verifiable with live
// credentials + a registered partner domain; treat as ready-to-test.
type teslaClient struct {
	authHost     string // https://auth.tesla.com
	authPath     string // /oauth2/v3
	fleetAPI     string // https://fleet-api.prd.<region>.vn.cloud.tesla.com
	clientID     string
	clientSecret string
	http         *http.Client
}

func newTeslaClient(authHost, authPath, fleetAPI, clientID, clientSecret string) *teslaClient {
	if authHost == "" {
		authHost = "https://auth.tesla.com"
	}
	if authPath == "" {
		authPath = "/oauth2/v3"
	}
	return &teslaClient{
		authHost: strings.TrimRight(authHost, "/"), authPath: authPath,
		fleetAPI: strings.TrimRight(fleetAPI, "/"), clientID: clientID, clientSecret: clientSecret,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *teslaClient) tokenURL() string { return c.authHost + c.authPath + "/token" }

// partnerToken gets a client-credentials token, required before registering the partner
// account. audience is the regional Fleet API base.
func (c *teslaClient) partnerToken() (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"scope":         {oauthScopes},
		"audience":      {c.fleetAPI},
	}
	return c.postToken(form)
}

// registerPartner registers the partner domain (Tesla then fetches the .well-known key).
func (c *teslaClient) registerPartner(domain string) error {
	tok, err := c.partnerToken()
	if err != nil {
		return fmt.Errorf("partner token: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"domain": domain})
	req, _ := http.NewRequest(http.MethodPost, c.fleetAPI+"/api/1/partner_accounts", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	return c.do2xx(req)
}

// authorizeURL builds the user-facing OAuth authorize URL (auth-code flow).
//
// Unused today: the onboarding wizard still requires a hand-pasted refresh
// token. This and exchangeCode are the two halves of the self-service OAuth
// authorization-code flow, so they are kept deliberately rather than deleted.
//
//nolint:unused // unfinished OAuth authorization-code flow, see above
func (c *teslaClient) authorizeURL(redirectURI, state string) string {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {c.clientID},
		"redirect_uri":  {redirectURI},
		"scope":         {oauthScopes},
		"state":         {state},
	}
	return c.authHost + c.authPath + "/authorize?" + q.Encode()
}

// exchangeCode swaps an auth code for tokens; returns the (rotating) refresh token.
//
// Unused today: the other half of the unfinished OAuth authorization-code flow.
// See authorizeURL.
//
//nolint:unused // unfinished OAuth authorization-code flow, see above
func (c *teslaClient) exchangeCode(code, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"audience":      {c.fleetAPI},
	}
	access, err := c.postTokenFull(form)
	if err != nil {
		return "", err
	}
	if access.Refresh == "" {
		return "", fmt.Errorf("no refresh_token in response (need offline_access scope)")
	}
	return access.Refresh, nil
}

// refresh exchanges a refresh token for an access token (for enroll calls).
func (c *teslaClient) refresh(refreshToken string) (string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.clientID},
		"refresh_token": {refreshToken},
	}
	if c.clientSecret != "" {
		form.Set("client_secret", c.clientSecret)
	}
	return c.postToken(form)
}

type teslaVehicle struct {
	VIN         string `json:"vin"`
	DisplayName string `json:"display_name"`
}

// vehicles lists the account's vehicles (VIN + name) using a user access token.
func (c *teslaClient) vehicles(accessToken string) ([]teslaVehicle, error) {
	req, _ := http.NewRequest(http.MethodGet, c.fleetAPI+"/api/1/vehicles", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var out struct {
		Response []teslaVehicle `json:"response"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, err
	}
	return out.Response, nil
}

// pairingURL is the per-account virtual-key pairing link; the owner opens it on a phone
// with the Tesla app installed to add the gateway's key to each vehicle.
func pairingURL(domain string) string {
	return "https://tesla.com/_ak/" + strings.TrimSuffix(domain, "/")
}

type tokenResponse struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
}

func (c *teslaClient) postToken(form url.Values) (string, error) {
	t, err := c.postTokenFull(form)
	if err != nil {
		return "", err
	}
	return t.Access, nil
}

func (c *teslaClient) postTokenFull(form url.Values) (tokenResponse, error) {
	var out tokenResponse
	resp, err := c.http.PostForm(c.tokenURL(), form)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("token status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return out, err
	}
	if out.Access == "" {
		return out, fmt.Errorf("no access_token in response")
	}
	return out, nil
}

func (c *teslaClient) do2xx(req *http.Request) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}
