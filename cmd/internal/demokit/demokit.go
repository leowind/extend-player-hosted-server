// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

// Package demokit holds the shared plumbing for the player-hosted demo binaries
// (demo-setup, demo-ds, demo-client). It wires the AccelByte Go SDK the same way
// main.go does, and provides small helpers for the two surfaces that are not
// covered by the SDK at this version: the Extend app's host registration API and
// the admin session-configuration endpoint (whose create model lacks the
// dsSource/appName/asyncProcessDSRequest fields a custom-DS template needs).
package demokit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/factory"
	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/repository"
	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/service/iam"
	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/service/session"
	sdkAuth "github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/utils/auth"
	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclient/game_session"
)

// Clients bundles the SDK services the demo binaries use over a shared token and
// config repository (built from the AB_BASE_URL/AB_CLIENT_ID/AB_CLIENT_SECRET/
// AB_NAMESPACE environment, exactly like main.go).
type Clients struct {
	OAuth      *iam.OAuth20Service
	Session    *session.GameSessionService
	ConfigTpl  *session.ConfigurationTemplateService
	TokenRepo  repository.TokenRepository
	ConfigRepo repository.ConfigRepository
}

// NewClients builds the SDK clients following the same pattern as main.go:166-181.
func NewClients() *Clients {
	tokenRepo := sdkAuth.DefaultTokenRepositoryImpl()
	configRepo := sdkAuth.DefaultConfigRepositoryImpl()
	refreshRepo := &sdkAuth.RefreshTokenImpl{RefreshRate: 0.8, AutoRefresh: true}

	sessionClient := factory.NewSessionClient(configRepo)

	return &Clients{
		OAuth: &iam.OAuth20Service{
			Client:                 factory.NewIamClient(configRepo),
			TokenRepository:        tokenRepo,
			RefreshTokenRepository: refreshRepo,
			ConfigRepository:       configRepo,
		},
		Session: &session.GameSessionService{
			Client:           sessionClient,
			TokenRepository:  tokenRepo,
			ConfigRepository: configRepo,
		},
		ConfigTpl: &session.ConfigurationTemplateService{
			Client:           sessionClient,
			TokenRepository:  tokenRepo,
			ConfigRepository: configRepo,
		},
		TokenRepo:  tokenRepo,
		ConfigRepo: configRepo,
	}
}

// LoginUser authenticates a player (password grant); the token lands in the
// shared TokenRepository and is used by both the SDK calls and AccessToken().
func (c *Clients) LoginUser(username, password string) error {
	return c.OAuth.LoginUser(username, password)
}

// LoginClient authenticates as the confidential client (client_credentials),
// used for the admin config-template calls.
func (c *Clients) LoginClient() error {
	id := c.ConfigRepo.GetClientId()
	secret := c.ConfigRepo.GetClientSecret()

	return c.OAuth.LoginClient(&id, &secret)
}

// AccessToken returns the current bearer token string for raw HTTP calls.
func (c *Clients) AccessToken() (string, error) {
	tok, err := c.TokenRepo.GetToken()
	if err != nil {
		return "", err
	}
	if tok.AccessToken == nil || *tok.AccessToken == "" {
		return "", fmt.Errorf("no access token available; login first")
	}

	return *tok.AccessToken, nil
}

// BaseURL is the AGS base URL from the SDK config repository.
func (c *Clients) BaseURL() string {
	return strings.TrimRight(c.ConfigRepo.GetJusticeBaseUrl(), "/")
}

// TemplateSpec describes the persistent, custom-DS, async session configuration
// template the demo needs. Exactly one of AppName / CustomURLGRPC should be set:
// AppName resolves the Extend app in-cluster via CSM; CustomURLGRPC points AGS at
// a direct gRPC URL (e.g. a tunnel to a locally-run Extend app).
type TemplateSpec struct {
	Name          string
	MaxPlayers    int
	ClientVersion string
	Deployment    string
	AppName       string
	CustomURLGRPC string
	// AsyncTimeout is the asyncProcessDSRequest timeout in seconds; AGS requires
	// 0 < timeout <= 600 when async is enabled.
	AsyncTimeout int
	// Persistent requests a persistent session. Note: the player-hosted provider
	// is async-only, and an unpatched Session Service rejects async+persistent
	// (see the proposal §7.1); set false to run against such a service.
	Persistent bool
}

// EnsureTemplate checks whether the named configuration template exists and
// creates it if not (idempotent). Creation goes through a raw admin HTTP POST
// because the SDK v0.85.0 create model omits dsSource/appName/asyncProcessDSRequest.
// Returns true when a template was created.
func EnsureTemplate(ctx context.Context, baseURL, namespace, token string, spec TemplateSpec) (bool, error) {
	getURL := fmt.Sprintf("%s/session/v1/admin/namespaces/%s/configurations/%s", baseURL, namespace, spec.Name)
	status, _, err := doJSON(ctx, http.MethodGet, getURL, token, nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusOK {
		return false, nil
	}
	if status != http.StatusNotFound {
		return false, fmt.Errorf("checking template %q: unexpected status %d", spec.Name, status)
	}

	body := map[string]interface{}{
		"name":                  spec.Name,
		"type":                  "DS",
		"persistent":            spec.Persistent,
		"joinability":           "OPEN",
		"minPlayers":            1,
		"maxPlayers":            spec.MaxPlayers,
		"clientVersion":         spec.ClientVersion,
		"deployment":            spec.Deployment,
		"dsSource":              "custom",
		"asyncProcessDSRequest": map[string]interface{}{"async": true, "timeout": spec.AsyncTimeout},
	}
	if spec.AppName != "" {
		body["appName"] = spec.AppName
	}
	if spec.CustomURLGRPC != "" {
		body["customURLGRPC"] = spec.CustomURLGRPC
	}

	postURL := fmt.Sprintf("%s/session/v1/admin/namespaces/%s/configuration", baseURL, namespace)
	status, respBody, err := doJSON(ctx, http.MethodPost, postURL, token, body)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return false, fmt.Errorf("creating template %q: status %d: %s", spec.Name, status, strings.TrimSpace(string(respBody)))
	}

	return true, nil
}

// Register reports the host's address to the Extend app's player-hosted
// registration endpoint. It returns the HTTP status code so callers can retry on
// 409 (session not yet awaiting registration).
func Register(ctx context.Context, regURL, namespace, sessionID, token, ip string, port int, serverID string) (int, error) {
	url := fmt.Sprintf("%s/playerhosted/v1/namespaces/%s/sessions/%s/register",
		strings.TrimRight(regURL, "/"), namespace, sessionID)
	body := map[string]interface{}{"ip": ip, "port": port, "serverId": serverID}

	status, respBody, err := doJSON(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return status, fmt.Errorf("register failed: status %d: %s", status, strings.TrimSpace(string(respBody)))
	}

	return status, nil
}

// Heartbeat pings the Extend app to keep the host marked alive.
func Heartbeat(ctx context.Context, regURL, namespace, sessionID, token string) (int, error) {
	url := fmt.Sprintf("%s/playerhosted/v1/namespaces/%s/sessions/%s/heartbeat",
		strings.TrimRight(regURL, "/"), namespace, sessionID)

	status, respBody, err := doJSON(ctx, http.MethodPost, url, token, nil)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return status, fmt.Errorf("heartbeat failed: status %d: %s", status, strings.TrimSpace(string(respBody)))
	}

	return status, nil
}

// PollDSInfo polls the game session until its DS information reports AVAILABLE
// with an address, and returns it. It fails fast on terminal DS statuses.
func PollDSInfo(ctx context.Context, sess *session.GameSessionService, namespace, sessionID string, interval time.Duration) (ip string, port int32, err error) {
	for {
		resp, gerr := sess.GetGameSessionShort(&game_session.GetGameSessionParams{
			Namespace: namespace,
			SessionID: sessionID,
			Context:   ctx,
		})
		if gerr != nil {
			return "", 0, fmt.Errorf("get game session: %w", gerr)
		}

		status := ""
		if resp != nil && resp.DSInformation != nil {
			status = resp.DSInformation.StatusV2
			if status == "" {
				status = resp.DSInformation.Status
			}
		}

		switch status {
		case "AVAILABLE":
			if resp.DSInformation.Server != nil && resp.DSInformation.Server.IP != "" {
				return resp.DSInformation.Server.IP, resp.DSInformation.Server.Port, nil
			}
		case "FAILED_TO_REQUEST", "DS_ERROR", "ENDED", "DS_CANCELLED":
			return "", 0, fmt.Errorf("DS request reached terminal status %q", status)
		}

		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// doJSON performs an authenticated JSON HTTP request and returns the status code
// and raw response body.
func doJSON(ctx context.Context, method, url, token string, payload interface{}) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, respBody, nil
}
