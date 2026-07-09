package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	linuxDoAuthorizeURL = "https://connect.linux.do/oauth2/authorize"
	linuxDoTokenURL     = "https://connect.linux.do/oauth2/token"
	linuxDoUserURL      = "https://connect.linux.do/api/user"
)

type linuxDoTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func (s *Server) linuxDoRedirectURI(r *http.Request) string {
	return strings.TrimRight(s.baseURL(r), "/") + "/api/u/auth/linuxdo/callback"
}

func (s *Server) linuxDoAuthorizationURL(r *http.Request, state string) string {
	values := url.Values{}
	values.Set("client_id", s.linuxDoClientID())
	values.Set("redirect_uri", s.linuxDoRedirectURI(r))
	values.Set("response_type", "code")
	values.Set("state", state)
	values.Set("scope", "read")
	return linuxDoAuthorizeURL + "?" + values.Encode()
}

func (s *Server) fetchLinuxDoProfile(ctx context.Context, r *http.Request, code string) (LinuxDoProfile, error) {
	clientID := s.linuxDoClientID()
	clientSecret := s.linuxDoClientSecret()
	if clientID == "" || clientSecret == "" {
		return LinuxDoProfile{}, errors.New("LinuxDo OAuth is not configured")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", s.linuxDoRedirectURI(r))
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	timeout := s.config.RequestTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linuxDoTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return LinuxDoProfile{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{Timeout: timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return LinuxDoProfile{}, err
	}
	defer resp.Body.Close()

	var token linuxDoTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return LinuxDoProfile{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || strings.TrimSpace(token.AccessToken) == "" {
		msg := firstNonEmpty(token.Description, token.Error, resp.Status)
		return LinuxDoProfile{}, fmt.Errorf("LinuxDo token exchange failed: %s", msg)
	}

	userReq, err := http.NewRequestWithContext(ctx, http.MethodGet, linuxDoUserURL, nil)
	if err != nil {
		return LinuxDoProfile{}, err
	}
	tokenType := strings.TrimSpace(token.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	userReq.Header.Set("Authorization", tokenType+" "+token.AccessToken)
	userResp, err := httpClient.Do(userReq)
	if err != nil {
		return LinuxDoProfile{}, err
	}
	defer userResp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(userResp.Body).Decode(&raw); err != nil {
		return LinuxDoProfile{}, err
	}
	if userResp.StatusCode < 200 || userResp.StatusCode >= 300 {
		return LinuxDoProfile{}, fmt.Errorf("LinuxDo user request failed: %s", userResp.Status)
	}
	return linuxDoProfileFromPayload(raw)
}

func linuxDoProfileFromPayload(raw map[string]any) (LinuxDoProfile, error) {
	payload := raw
	if nested, ok := raw["user"].(map[string]any); ok {
		payload = nested
	}
	profile := LinuxDoProfile{
		ID:          stringFromAny(payload["id"]),
		Username:    firstNonEmpty(stringFromAny(payload["username"]), stringFromAny(payload["login"]), stringFromAny(payload["name"])),
		Name:        stringFromAny(payload["name"]),
		DisplayName: firstNonEmpty(stringFromAny(payload["display_name"]), stringFromAny(payload["nickname"]), stringFromAny(payload["name"]), stringFromAny(payload["username"])),
		Email:       stringFromAny(payload["email"]),
		AvatarURL:   firstNonEmpty(stringFromAny(payload["avatar_url"]), stringFromAny(payload["avatar"]), stringFromAny(payload["picture"])),
	}
	if strings.TrimSpace(profile.ID) == "" {
		return LinuxDoProfile{}, errors.New("LinuxDo user response did not include an id")
	}
	return profile, nil
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}
