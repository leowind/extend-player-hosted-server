// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"extend-player-hosted-server/pkg/client/playerhosted"
	"extend-player-hosted-server/pkg/common"
	"extend-player-hosted-server/pkg/config"
	"extend-player-hosted-server/pkg/utils/envelope"

	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclient/game_session"
	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclientmodels"
)

// SessionReader is the slice of the AGS session client used to authorize a host
// against the session's membership.
type SessionReader interface {
	GetGameSessionShort(input *game_session.GetGameSessionParams) (*sessionclientmodels.ApimodelsGameSessionResponse, error)
}

// RegistrationHandler serves the host-facing registration/heartbeat HTTP API.
// It is deliberately separate from the (internal) gRPC surface AGS dials: host
// players call these endpoints directly, so they must be exposed via ingress.
type RegistrationHandler struct {
	registry *playerhosted.Registry
	sessions SessionReader
	cfg      *config.Config
}

// NewRegistrationHandler wires the handler.
func NewRegistrationHandler(registry *playerhosted.Registry, sessions SessionReader, cfg *config.Config) *RegistrationHandler {
	return &RegistrationHandler{registry: registry, sessions: sessions, cfg: cfg}
}

// registerRequest is the host-provided body for the register endpoint.
type registerRequest struct {
	IP       string `json:"ip"`
	Port     int32  `json:"port"`
	ServerID string `json:"serverId"`
}

// Mux returns an http.ServeMux with the registration routes bound.
func (h *RegistrationHandler) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /playerhosted/v1/namespaces/{namespace}/sessions/{sessionId}/register", h.handleRegister)
	mux.HandleFunc("POST /playerhosted/v1/namespaces/{namespace}/sessions/{sessionId}/heartbeat", h.handleHeartbeat)

	return mux
}

func (h *RegistrationHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
	scope := envelope.NewRootScope(r.Context(), "playerhosted.Register", "")
	defer scope.Finish()

	namespace := r.PathValue("namespace")
	sessionID := r.PathValue("sessionId")

	callerID, err := h.authorizeToken(r, namespace)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())

		return
	}

	var body registerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")

		return
	}
	if body.IP == "" || body.Port == 0 {
		writeError(w, http.StatusBadRequest, "ip and port are required")

		return
	}

	if code, err := h.register(scope, namespace, sessionID, callerID, body); err != nil {
		writeError(w, code, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "registered"})
}

func (h *RegistrationHandler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	scope := envelope.NewRootScope(r.Context(), "playerhosted.Heartbeat", "")
	defer scope.Finish()

	namespace := r.PathValue("namespace")
	sessionID := r.PathValue("sessionId")

	callerID, err := h.authorizeToken(r, namespace)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())

		return
	}

	if code, err := h.authorizeMembership(scope, namespace, sessionID, callerID); err != nil {
		writeError(w, code, err.Error())

		return
	}

	if err := h.registry.Heartbeat(sessionID); err != nil {
		writeError(w, http.StatusConflict, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// register authorizes the caller against the session, probes the reported
// address for reachability, then binds it and flips the session AVAILABLE. It
// returns an HTTP status code alongside any error.
func (h *RegistrationHandler) register(scope *envelope.Scope, namespace, sessionID, callerID string, body registerRequest) (int, error) {
	if code, err := h.authorizeMembership(scope, namespace, sessionID, callerID); err != nil {
		return code, err
	}

	addr := net.JoinHostPort(body.IP, strconv.Itoa(int(body.Port)))
	conn, err := net.DialTimeout("tcp", addr, h.cfg.PlayerHostedReachabilityTimeout)
	if err != nil {
		scope.Log.Warn("player-hosted: host address unreachable", "sessionID", sessionID, "addr", addr, "error", err)

		return http.StatusUnprocessableEntity, fmt.Errorf("host address %s is not reachable", addr)
	}
	_ = conn.Close()

	if err := h.registry.RegisterHost(scope, namespace, sessionID, body.IP, body.Port, body.ServerID); err != nil {
		return http.StatusConflict, err
	}

	return http.StatusOK, nil
}

// authorizeMembership confirms callerID is the leader or a member of the target
// session. Never let an arbitrary caller bind an address to someone else's
// session (proposal §5.3).
func (h *RegistrationHandler) authorizeMembership(scope *envelope.Scope, namespace, sessionID, callerID string) (int, error) {
	resp, err := h.sessions.GetGameSessionShort(&game_session.GetGameSessionParams{
		Namespace: namespace,
		SessionID: sessionID,
		Context:   scope.Ctx,
	})
	if err != nil {
		scope.Log.Error("player-hosted: failed to fetch session for authorization", "error", err, "sessionID", sessionID)

		return http.StatusNotFound, fmt.Errorf("session %s not found", sessionID)
	}

	if isSessionMember(resp, callerID) {
		return http.StatusOK, nil
	}

	scope.Log.Warn("player-hosted: caller is not a member of the session",
		"sessionID", sessionID, "callerID", callerID)

	return http.StatusForbidden, fmt.Errorf("caller is not authorized for session %s", sessionID)
}

// authorizeToken validates the bearer token and returns the caller's userID.
func (h *RegistrationHandler) authorizeToken(r *http.Request, namespace string) (string, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		return "", fmt.Errorf("authorization token is missing")
	}

	if common.Validator != nil {
		ns := namespace
		if err := common.Validator.Validate(token, nil, &ns, nil); err != nil {
			return "", fmt.Errorf("invalid token: %w", err)
		}
	}

	callerID, err := subjectFromJWT(token)
	if err != nil {
		return "", err
	}

	return callerID, nil
}

func isSessionMember(resp *sessionclientmodels.ApimodelsGameSessionResponse, callerID string) bool {
	if resp == nil || callerID == "" {
		return false
	}
	if resp.LeaderID != nil && *resp.LeaderID == callerID {
		return true
	}
	for _, m := range resp.Members {
		if m != nil && m.ID != nil && *m.ID == callerID {
			return true
		}
	}

	return false
}

// subjectFromJWT extracts the "sub" (userID) claim from a JWT without verifying
// the signature (validation happens via common.Validator).
func subjectFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("malformed token payload")
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Sub == "" {
		return "", fmt.Errorf("token has no subject")
	}

	return claims.Sub, nil
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
