// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

// Package server (playerhosted) implements the session-dsm gRPC contract for the
// player-hosted (self-host / listen-server) model. There is no fleet to
// allocate: AGS requests a DS asynchronously, the app waits for the host player
// to register its public IP:port (see registration.go), then reports readiness
// to AGS via the dsinformation callback.
package server

import (
	"context"
	"errors"

	"extend-player-hosted-server/pkg/client/playerhosted"
	sessiondsm "extend-player-hosted-server/pkg/pb"
	"extend-player-hosted-server/pkg/utils/envelope"
)

// SessionDSM implements sessiondsm.SessionDsmServer for player-hosted servers.
type SessionDSM struct {
	sessiondsm.UnimplementedSessionDsmServer
	Registry *playerhosted.Registry
}

// CreateGameSession is not supported for player-hosted servers: the host address
// is not known at request time, so there is nothing to return inline. The
// session template must enable async DS requests.
func (s *SessionDSM) CreateGameSession(ctx context.Context, req *sessiondsm.RequestCreateGameSession) (*sessiondsm.ResponseCreateGameSession, error) {
	scope := envelope.NewRootScope(ctx, "CreateGameSession", "")
	defer scope.Finish()

	scope.Log.Warn("player-hosted: synchronous CreateGameSession is not supported", "sessionID", req.SessionId)

	return nil, errors.New("player-hosted DS requires an async DS request (enable asyncProcessDSRequest); the host registers its address out-of-band")
}

// CreateGameSessionAsync accepts the request and marks the session as awaiting
// host registration. Readiness is reported later, once the host registers.
func (s *SessionDSM) CreateGameSessionAsync(ctx context.Context, req *sessiondsm.RequestCreateGameSession) (*sessiondsm.ResponseCreateGameSessionAsync, error) {
	scope := envelope.NewRootScope(ctx, "CreateGameSessionAsync", "")
	defer scope.Finish()

	scope.Log.Info("player-hosted: awaiting host registration",
		"sessionID", req.SessionId, "namespace", req.Namespace)

	s.Registry.AwaitRegistration(scope, req.SessionId, req.Namespace)

	return &sessiondsm.ResponseCreateGameSessionAsync{
		Success: true,
		Message: "awaiting host registration",
	}, nil
}

// TerminateGameSession forgets the session. There is no hardware to reclaim for
// a player-hosted server.
func (s *SessionDSM) TerminateGameSession(ctx context.Context, req *sessiondsm.RequestTerminateGameSession) (*sessiondsm.ResponseTerminateGameSession, error) {
	scope := envelope.NewRootScope(ctx, "TerminateGameSession", "")
	defer scope.Finish()

	s.Registry.Release(scope, req.SessionId)

	return &sessiondsm.ResponseTerminateGameSession{
		SessionId: req.SessionId,
		Namespace: req.Namespace,
		Success:   true,
	}, nil
}
