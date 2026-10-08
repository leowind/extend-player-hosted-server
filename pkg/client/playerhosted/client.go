// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

// Package playerhosted implements the state store, DS-information callbacks and
// lifecycle policies for player-hosted (self-host / listen-server) persistent
// sessions. There is no fleet to allocate from: the host player's client
// registers its own public IP:port, which the registry validates and pushes to
// AGS Session Service via the dsinformation callback.
//
// Persistent sessions never expire on their own in AGS, so this registry also
// owns their lifecycle: it deletes sessions that stay hostless or empty for too
// long (see sweep).
package playerhosted

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"extend-player-hosted-server/pkg/config"
	"extend-player-hosted-server/pkg/constants"
	"extend-player-hosted-server/pkg/utils/envelope"

	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclient/game_session"
	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclientmodels"
)

// SessionUpdater is the slice of the AGS session client the registry needs to
// report DS information back to the Session Service, inspect session members
// and delete sessions it has decided to end.
type SessionUpdater interface {
	AdminUpdateDSInformationShort(input *game_session.AdminUpdateDSInformationParams) error
	GetGameSessionShort(input *game_session.GetGameSessionParams) (*sessionclientmodels.ApimodelsGameSessionResponse, error)
	AdminDeleteBulkGameSessionsShort(input *game_session.AdminDeleteBulkGameSessionsParams) (*sessionclientmodels.ApimodelsDeleteBulkGameSessionsAPIResponse, error)
}

const (
	statusAwaiting  = "AWAITING_REGISTRATION"
	statusAvailable = "AVAILABLE"
	statusError     = "ERROR"
	statusFailed    = "FAILED"
)

// hostState tracks a single persistent session's player-hosted server.
type hostState struct {
	namespace     string
	ip            string
	port          int32
	serverID      string
	status        string
	lastHeartbeat time.Time
	regTimer      *time.Timer
	// hostlessSince is when the session last lost (or never had) a live host;
	// zero while a host is registered and heartbeating.
	hostlessSince time.Time
	// emptySince is when the session was first seen with no JOINED/CONNECTED
	// members while AVAILABLE; zero otherwise.
	emptySince time.Time
	// deleting is set while the registry is deleting the AGS session, so the
	// host cannot (re-)register or heartbeat in the meantime.
	deleting bool
}

// Registry holds the in-memory state of every player-hosted session this app is
// currently brokering. It is safe for concurrent use.
type Registry struct {
	mu            sync.Mutex
	sessions      map[string]*hostState
	sessionClient SessionUpdater
	cfg           *config.Config
}

// New builds an empty registry.
func New(cfg *config.Config, sessionClient SessionUpdater) *Registry {
	return &Registry{
		sessions:      make(map[string]*hostState),
		sessionClient: sessionClient,
		cfg:           cfg,
	}
}

// AwaitRegistration records a session as waiting for its host to register and
// arms a timeout: if no host registers within PlayerHostedRegTimeout the DS
// request is failed via the dsinformation callback. A session that was already
// hostless (e.g. a DS re-request after heartbeat loss) keeps its earlier
// hostlessSince, so re-requests do not postpone the hostless cleanup.
func (r *Registry) AwaitRegistration(scope *envelope.Scope, sessionID, namespace string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	st, ok := r.sessions[sessionID]
	if !ok {
		st = &hostState{}
		r.sessions[sessionID] = st
	}
	if st.regTimer != nil {
		st.regTimer.Stop()
	}
	st.namespace = namespace
	st.ip = ""
	st.port = 0
	st.serverID = ""
	st.status = statusAwaiting
	st.emptySince = time.Time{}
	if st.hostlessSince.IsZero() {
		st.hostlessSince = now
	}
	st.regTimer = time.AfterFunc(r.cfg.PlayerHostedRegTimeout, func() {
		r.failIfStillAwaiting(sessionID, namespace)
	})

	scope.Log.Info("player-hosted: awaiting host registration",
		"sessionID", sessionID, "namespace", namespace, "timeout", r.cfg.PlayerHostedRegTimeout.String(),
		"hostlessSince", st.hostlessSince.Format(time.RFC3339))
}

// RegisterHost binds a validated host address to the session and flips it to
// AVAILABLE via the dsinformation callback. Callers must have already
// authorized the host and probed reachability.
func (r *Registry) RegisterHost(scope *envelope.Scope, namespace, sessionID, ip string, port int32, serverID string) error {
	r.mu.Lock()
	st, ok := r.sessions[sessionID]
	if !ok || st.status == statusFailed || st.deleting {
		// Session was not awaiting registration (e.g. never requested, already
		// terminated, timed out, or being cleaned up). Reject rather than
		// silently create it; the next DS request re-arms registration.
		r.mu.Unlock()

		return fmt.Errorf("session %s is not awaiting host registration", sessionID)
	}
	if st.regTimer != nil {
		st.regTimer.Stop()
		st.regTimer = nil
	}
	st.namespace = namespace
	st.ip = ip
	st.port = port
	st.serverID = serverID
	st.status = statusAvailable
	st.lastHeartbeat = time.Now()
	st.hostlessSince = time.Time{}
	st.emptySince = time.Time{}
	r.mu.Unlock()

	description := "player-hosted server"
	if err := r.pushDSInfo(scope, namespace, sessionID, constants.DSStatusAvailable,
		constants.GameServerSourceCustom, ip, port, serverID, description); err != nil {
		scope.Log.Error("player-hosted: failed to publish DS information", "error", err, "sessionID", sessionID)

		return err
	}

	scope.Log.Info("player-hosted: host registered, session AVAILABLE",
		"sessionID", sessionID, "ip", ip, "port", port, "serverID", serverID)

	return nil
}

// Heartbeat refreshes the last-seen time for an active host. It returns an error
// for sessions that are unknown, not yet AVAILABLE, or being cleaned up.
func (r *Registry) Heartbeat(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	st, ok := r.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s is not registered", sessionID)
	}
	if st.deleting {
		return fmt.Errorf("session %s is being ended", sessionID)
	}
	if st.status != statusAvailable {
		return fmt.Errorf("session %s has no active host (status=%s)", sessionID, st.status)
	}
	st.lastHeartbeat = time.Now()

	return nil
}

// Release forgets a session (on TerminateGameSession). There is no hardware to
// reclaim for a player-hosted server.
func (r *Registry) Release(scope *envelope.Scope, sessionID string) {
	r.forget(sessionID)

	scope.Log.Info("player-hosted: session released", "sessionID", sessionID)
}

// StartHeartbeatMonitor runs until ctx is cancelled, sweeping every
// PlayerHostedHeartbeatInterval to enforce the session lifecycle:
//
//   - an active host that stopped heartbeating is reported as DS_ERROR; because
//     the AGS session is persistent, AGS keeps it and re-requests a DS (which
//     re-arms host registration);
//   - a session with no live host for PlayerHostedHostlessSessionTimeout is
//     deleted from AGS;
//   - an AVAILABLE session with no JOINED/CONNECTED members for
//     PlayerHostedEmptySessionTimeout is deleted from AGS.
//
// TODO(host-migration): on heartbeat loss, promote another session member as
// the new host and await their registration (proposal §5.2). Out of scope here.
func (r *Registry) StartHeartbeatMonitor(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PlayerHostedHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep()
		}
	}
}

// sessionRef identifies a session picked up by a sweep.
type sessionRef struct {
	sessionID string
	namespace string
}

// sweep runs one pass of the lifecycle policies. AGS calls are made without
// holding the lock; state is re-checked under the lock before acting on it.
func (r *Registry) sweep() {
	now := time.Now()

	var staleHosts, hostless, available []sessionRef

	r.mu.Lock()
	for sessionID, st := range r.sessions {
		if st.deleting {
			continue
		}
		ref := sessionRef{sessionID: sessionID, namespace: st.namespace}
		if st.status == statusAvailable && now.Sub(st.lastHeartbeat) > r.cfg.PlayerHostedHeartbeatTimeout {
			st.status = statusError
			st.hostlessSince = now
			st.emptySince = time.Time{}
			staleHosts = append(staleHosts, ref)
		}
		switch {
		case st.status != statusAvailable && r.cfg.PlayerHostedHostlessSessionTimeout > 0 &&
			!st.hostlessSince.IsZero() && now.Sub(st.hostlessSince) >= r.cfg.PlayerHostedHostlessSessionTimeout:
			st.deleting = true
			hostless = append(hostless, ref)
		case st.status == statusAvailable && r.cfg.PlayerHostedEmptySessionTimeout > 0:
			available = append(available, ref)
		}
	}
	r.mu.Unlock()

	for _, s := range staleHosts {
		scope := envelope.NewRootScope(context.Background(), "playerhosted.HeartbeatMonitor", "")
		scope.Log.Warn("player-hosted: host heartbeat lost, reporting DS_ERROR", "sessionID", s.sessionID)
		if err := r.pushDSInfo(scope, s.namespace, s.sessionID, constants.DSStatusError,
			constants.GameServerSourceCustom, "", 0, "", "player-hosted heartbeat lost"); err != nil {
			scope.Log.Error("player-hosted: failed to report DS_ERROR", "error", err, "sessionID", s.sessionID)
		}
		scope.Finish()
	}

	for _, s := range hostless {
		r.endSession(s, "no live host within "+r.cfg.PlayerHostedHostlessSessionTimeout.String())
	}

	for _, s := range available {
		if r.checkEmpty(s, now) {
			r.endSession(s, "no active members within "+r.cfg.PlayerHostedEmptySessionTimeout.String())
		}
	}
}

// checkEmpty fetches the session's members and updates emptySince. It returns
// true (and marks the session as deleting) once the session has had no
// JOINED/CONNECTED members for PlayerHostedEmptySessionTimeout.
func (r *Registry) checkEmpty(s sessionRef, now time.Time) bool {
	scope := envelope.NewRootScope(context.Background(), "playerhosted.EmptySessionCheck", "")
	defer scope.Finish()

	resp, err := r.sessionClient.GetGameSessionShort(&game_session.GetGameSessionParams{
		Namespace: s.namespace,
		SessionID: s.sessionID,
		Context:   scope.Ctx,
	})
	if err != nil {
		scope.Log.Warn("player-hosted: failed to fetch session members", "error", err, "sessionID", s.sessionID)

		return false
	}
	active := hasActiveMember(resp)

	r.mu.Lock()
	defer r.mu.Unlock()

	st, ok := r.sessions[s.sessionID]
	if !ok || st.deleting || st.status != statusAvailable {
		return false
	}
	if active {
		st.emptySince = time.Time{}

		return false
	}
	if st.emptySince.IsZero() {
		st.emptySince = now
		scope.Log.Info("player-hosted: session has no active members", "sessionID", s.sessionID,
			"timeout", r.cfg.PlayerHostedEmptySessionTimeout.String())

		return false
	}
	if now.Sub(st.emptySince) < r.cfg.PlayerHostedEmptySessionTimeout {
		return false
	}
	st.deleting = true

	return true
}

// endSession deletes the AGS session and forgets it locally. On failure the
// session is kept and the next sweep tries again.
func (r *Registry) endSession(s sessionRef, reason string) {
	scope := envelope.NewRootScope(context.Background(), "playerhosted.EndSession", "")
	defer scope.Finish()

	scope.Log.Warn("player-hosted: ending persistent session", "sessionID", s.sessionID, "reason", reason)
	if err := r.deleteSession(scope, s.namespace, s.sessionID); err != nil {
		scope.Log.Error("player-hosted: failed to delete session, will retry", "error", err, "sessionID", s.sessionID)

		r.mu.Lock()
		if st, ok := r.sessions[s.sessionID]; ok {
			st.deleting = false
		}
		r.mu.Unlock()

		return
	}

	r.forget(s.sessionID)
	scope.Log.Info("player-hosted: session deleted", "sessionID", s.sessionID)
}

// forget drops a session from the registry and stops its registration timer.
func (r *Registry) forget(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if st, ok := r.sessions[sessionID]; ok && st.regTimer != nil {
		st.regTimer.Stop()
	}
	delete(r.sessions, sessionID)
}

// failIfStillAwaiting is the registration-timeout callback: if the session is
// still waiting for a host, report FAILED_TO_REQUEST and mark it failed. The
// entry is kept (still hostless) so the hostless cleanup can end the session
// if AGS never brings a host back.
func (r *Registry) failIfStillAwaiting(sessionID, namespace string) {
	r.mu.Lock()
	st, ok := r.sessions[sessionID]
	if !ok || st.status != statusAwaiting {
		r.mu.Unlock()

		return
	}
	st.status = statusFailed
	st.regTimer = nil
	r.mu.Unlock()

	scope := envelope.NewRootScope(context.Background(), "playerhosted.RegistrationTimeout", "")
	defer scope.Finish()
	scope.Log.Warn("player-hosted: no host registered before timeout, reporting FAILED_TO_REQUEST", "sessionID", sessionID)
	if err := r.pushDSInfo(scope, namespace, sessionID, constants.DSStatusFailedToRequest,
		constants.GameServerSourceCustom, "", 0, "", "no host registered before timeout"); err != nil {
		scope.Log.Error("player-hosted: failed to report FAILED_TO_REQUEST", "error", err, "sessionID", sessionID)
	}
}

// pushDSInfo reports DS information to the AGS Session Service. ip/port/serverID
// are only included when non-empty (error/failure reports omit them).
func (r *Registry) pushDSInfo(scope *envelope.Scope, namespace, sessionID, dsStatus, source, ip string, port int32, serverID, description string) error {
	child := scope.NewChildScope("playerhosted.UpdateDSInformation")
	defer child.Finish()

	status := dsStatus
	src := source
	desc := description
	body := &sessionclientmodels.ApimodelsUpdateGamesessionDSInformationRequest{
		Status:      &status,
		Source:      &src,
		Description: &desc,
	}
	if ip != "" {
		ipVal := ip
		portVal := port
		body.IP = &ipVal
		body.Port = &portVal
	}
	if serverID != "" {
		serverIDVal := serverID
		body.ServerID = &serverIDVal
	}

	return r.sessionClient.AdminUpdateDSInformationShort(&game_session.AdminUpdateDSInformationParams{
		Namespace: namespace,
		SessionID: sessionID,
		Context:   child.Ctx,
		Body:      body,
	})
}

// deleteSession deletes the AGS game session. Requires the app's client to have
// ADMIN:NAMESPACE:{namespace}:SESSION:GAME [DELETE].
func (r *Registry) deleteSession(scope *envelope.Scope, namespace, sessionID string) error {
	child := scope.NewChildScope("playerhosted.DeleteGameSession")
	defer child.Finish()

	resp, err := r.sessionClient.AdminDeleteBulkGameSessionsShort(&game_session.AdminDeleteBulkGameSessionsParams{
		Namespace: namespace,
		Body:      &sessionclientmodels.ApimodelsDeleteBulkGameSessionRequest{Ids: []string{sessionID}},
		Context:   child.Ctx,
	})
	if err != nil {
		return err
	}
	if resp != nil && len(resp.Failed) > 0 {
		return fmt.Errorf("session service did not delete session %s", sessionID)
	}

	return nil
}

// hasActiveMember reports whether any member is JOINED or CONNECTED.
func hasActiveMember(resp *sessionclientmodels.ApimodelsGameSessionResponse) bool {
	if resp == nil {
		return false
	}
	for _, m := range resp.Members {
		if m != nil && (isActiveMemberStatus(m.StatusV2) || isActiveMemberStatus(m.Status)) {
			return true
		}
	}

	return false
}

func isActiveMemberStatus(status *string) bool {
	return status != nil && (strings.EqualFold(*status, "JOINED") || strings.EqualFold(*status, "CONNECTED"))
}
