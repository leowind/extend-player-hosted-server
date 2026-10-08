// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

package server

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclient/game_session"
	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclientmodels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"extend-player-hosted-server/pkg/client/playerhosted"
	"extend-player-hosted-server/pkg/config"
	sessiondsm "extend-player-hosted-server/pkg/pb"
	"extend-player-hosted-server/pkg/utils/envelope"
)

// waitWithTimeout waits for wg to reach zero or fails the test after timeout.
func waitWithTimeout(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for async worker to complete")
	}
}

// MockSessionUpdater mocks playerhosted.SessionUpdater.
type MockSessionUpdater struct {
	mock.Mock
}

func (m *MockSessionUpdater) AdminUpdateDSInformationShort(params *game_session.AdminUpdateDSInformationParams) error {
	args := m.Called(params)

	return args.Error(0)
}

func (m *MockSessionUpdater) GetGameSessionShort(params *game_session.GetGameSessionParams) (*sessionclientmodels.ApimodelsGameSessionResponse, error) {
	args := m.Called(params)
	var resp *sessionclientmodels.ApimodelsGameSessionResponse
	if args.Get(0) != nil {
		resp = args.Get(0).(*sessionclientmodels.ApimodelsGameSessionResponse)
	}

	return resp, args.Error(1)
}

func (m *MockSessionUpdater) AdminDeleteBulkGameSessionsShort(params *game_session.AdminDeleteBulkGameSessionsParams) (*sessionclientmodels.ApimodelsDeleteBulkGameSessionsAPIResponse, error) {
	args := m.Called(params)
	var resp *sessionclientmodels.ApimodelsDeleteBulkGameSessionsAPIResponse
	if args.Get(0) != nil {
		resp = args.Get(0).(*sessionclientmodels.ApimodelsDeleteBulkGameSessionsAPIResponse)
	}

	return resp, args.Error(1)
}

// MockSessionReader mocks SessionReader.
type MockSessionReader struct {
	mock.Mock
}

func (m *MockSessionReader) GetGameSessionShort(params *game_session.GetGameSessionParams) (*sessionclientmodels.ApimodelsGameSessionResponse, error) {
	args := m.Called(params)
	var resp *sessionclientmodels.ApimodelsGameSessionResponse
	if args.Get(0) != nil {
		resp = args.Get(0).(*sessionclientmodels.ApimodelsGameSessionResponse)
	}

	return resp, args.Error(1)
}

func testConfig() *config.Config {
	return &config.Config{
		PlayerHostedRegTimeout:          time.Minute,
		PlayerHostedReachabilityTimeout: 2 * time.Second,
		PlayerHostedHeartbeatInterval:   5 * time.Millisecond,
		PlayerHostedHeartbeatTimeout:    10 * time.Millisecond,
	}
}

func sessionWithLeader(leaderID string) *sessionclientmodels.ApimodelsGameSessionResponse {
	return &sessionclientmodels.ApimodelsGameSessionResponse{LeaderID: &leaderID}
}

func sessionWithMembers(statuses ...string) *sessionclientmodels.ApimodelsGameSessionResponse {
	resp := &sessionclientmodels.ApimodelsGameSessionResponse{}
	for i, status := range statuses {
		id := "user-" + string(rune('a'+i))
		statusV2 := status
		resp.Members = append(resp.Members, &sessionclientmodels.ApimodelsUserResponse{ID: &id, StatusV2: &statusV2})
	}

	return resp
}

func deleteMatcher(sessionID string) func(*game_session.AdminDeleteBulkGameSessionsParams) bool {
	return func(p *game_session.AdminDeleteBulkGameSessionsParams) bool {
		return p.Body != nil && len(p.Body.Ids) == 1 && p.Body.Ids[0] == sessionID
	}
}

func statusMatcher(status string) func(*game_session.AdminUpdateDSInformationParams) bool {
	return func(p *game_session.AdminUpdateDSInformationParams) bool {
		return p.Body != nil && p.Body.Status != nil && *p.Body.Status == status
	}
}

func TestCreateGameSession_Unsupported(t *testing.T) {
	server := &SessionDSM{Registry: playerhosted.New(testConfig(), new(MockSessionUpdater))}

	resp, err := server.CreateGameSession(context.Background(), &sessiondsm.RequestCreateGameSession{SessionId: "sess-1"})

	assert.Error(t, err)
	assert.Nil(t, resp)
}

func TestCreateGameSessionAsync_AwaitsRegistration(t *testing.T) {
	registry := playerhosted.New(testConfig(), new(MockSessionUpdater))
	server := &SessionDSM{Registry: registry}

	resp, err := server.CreateGameSessionAsync(context.Background(), &sessiondsm.RequestCreateGameSession{
		SessionId: "sess-1",
		Namespace: "ns",
	})

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, resp.Success)

	// Drop the awaiting timer so it does not linger.
	registry.Release(envelope.NewRootScope(context.Background(), "cleanup", ""), "sess-1")
}

func TestTerminateGameSession_Success(t *testing.T) {
	registry := playerhosted.New(testConfig(), new(MockSessionUpdater))
	server := &SessionDSM{Registry: registry}

	resp, err := server.TerminateGameSession(context.Background(), &sessiondsm.RequestTerminateGameSession{
		SessionId: "sess-1",
		Namespace: "ns",
	})

	assert.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestRegister_Success(t *testing.T) {
	updater := new(MockSessionUpdater)
	reader := new(MockSessionReader)
	cfg := testConfig()
	registry := playerhosted.New(cfg, updater)
	handler := NewRegistrationHandler(registry, reader, cfg)

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-1", "ns")

	reader.On("GetGameSessionShort", mock.Anything).Return(sessionWithLeader("user-1"), nil)
	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(func(p *game_session.AdminUpdateDSInformationParams) bool {
		return p.SessionID == "sess-1" && statusMatcher("AVAILABLE")(p) &&
			p.Body.Source != nil && *p.Body.Source == "custom"
	})).Return(nil)

	// Real listener acts as the reachable host server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	code, err := handler.register(scope, "ns", "sess-1", "user-1", registerRequest{
		IP:       "127.0.0.1",
		Port:     int32(port),
		ServerID: "host-1",
	})

	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)
	reader.AssertExpectations(t)
	updater.AssertExpectations(t)
}

func TestRegister_Unauthorized(t *testing.T) {
	updater := new(MockSessionUpdater)
	reader := new(MockSessionReader)
	cfg := testConfig()
	registry := playerhosted.New(cfg, updater)
	handler := NewRegistrationHandler(registry, reader, cfg)

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-1", "ns")

	// Session leader is someone else, and caller is not a member.
	reader.On("GetGameSessionShort", mock.Anything).Return(sessionWithLeader("another-user"), nil)

	code, err := handler.register(scope, "ns", "sess-1", "user-1", registerRequest{
		IP:       "127.0.0.1",
		Port:     7777,
		ServerID: "host-1",
	})

	assert.Error(t, err)
	assert.Equal(t, http.StatusForbidden, code)
	updater.AssertNotCalled(t, "AdminUpdateDSInformationShort", mock.Anything)
}

func TestRegister_Unreachable(t *testing.T) {
	updater := new(MockSessionUpdater)
	reader := new(MockSessionReader)
	cfg := testConfig()
	registry := playerhosted.New(cfg, updater)
	handler := NewRegistrationHandler(registry, reader, cfg)

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-1", "ns")

	reader.On("GetGameSessionShort", mock.Anything).Return(sessionWithLeader("user-1"), nil)

	// Grab a free port then release it so the dial is refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	closedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	code, err := handler.register(scope, "ns", "sess-1", "user-1", registerRequest{
		IP:       "127.0.0.1",
		Port:     int32(closedPort),
		ServerID: "host-1",
	})

	assert.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	updater.AssertNotCalled(t, "AdminUpdateDSInformationShort", mock.Anything)
}

func TestHeartbeatLoss_ReportsDSError(t *testing.T) {
	updater := new(MockSessionUpdater)
	cfg := testConfig()
	registry := playerhosted.New(cfg, updater)

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-hb", "ns")

	var wg sync.WaitGroup
	wg.Add(1)
	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("AVAILABLE"))).Return(nil)
	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("DS_ERROR"))).
		Return(nil).Run(func(_ mock.Arguments) { wg.Done() })

	assert.NoError(t, registry.RegisterHost(scope, "ns", "sess-hb", "127.0.0.1", 7777, "host-1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go registry.StartHeartbeatMonitor(ctx)

	waitWithTimeout(t, &wg, 2*time.Second)
	updater.AssertExpectations(t)
}

func TestRegistrationTimeout_KeepsFailedEntryAndRejectsLateHost(t *testing.T) {
	updater := new(MockSessionUpdater)
	cfg := testConfig()
	cfg.PlayerHostedRegTimeout = 10 * time.Millisecond
	registry := playerhosted.New(cfg, updater)

	var wg sync.WaitGroup
	wg.Add(1)
	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("FAILED_TO_REQUEST"))).
		Return(nil).Run(func(_ mock.Arguments) { wg.Done() })

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-late", "ns")
	waitWithTimeout(t, &wg, 2*time.Second)

	// A host showing up after the timeout is rejected until AGS re-requests a DS.
	assert.Error(t, registry.RegisterHost(scope, "ns", "sess-late", "127.0.0.1", 7777, "host-1"))
	updater.AssertNotCalled(t, "AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("AVAILABLE")))
}

func TestHostlessSession_NeverRegistered_IsDeleted(t *testing.T) {
	updater := new(MockSessionUpdater)
	cfg := testConfig()
	cfg.PlayerHostedHostlessSessionTimeout = 20 * time.Millisecond
	registry := playerhosted.New(cfg, updater)

	var wg sync.WaitGroup
	wg.Add(1)
	updater.On("AdminDeleteBulkGameSessionsShort", mock.MatchedBy(deleteMatcher("sess-hl"))).
		Return(&sessionclientmodels.ApimodelsDeleteBulkGameSessionsAPIResponse{}, nil).
		Run(func(_ mock.Arguments) { wg.Done() })

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-hl", "ns")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go registry.StartHeartbeatMonitor(ctx)

	waitWithTimeout(t, &wg, 2*time.Second)
	assert.Error(t, registry.RegisterHost(scope, "ns", "sess-hl", "127.0.0.1", 7777, "host-1"))
	updater.AssertExpectations(t)
}

func TestHostlessSession_AfterHeartbeatLoss_IsDeleted(t *testing.T) {
	updater := new(MockSessionUpdater)
	cfg := testConfig()
	cfg.PlayerHostedHostlessSessionTimeout = 30 * time.Millisecond
	registry := playerhosted.New(cfg, updater)

	var wg sync.WaitGroup
	wg.Add(2)
	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("AVAILABLE"))).Return(nil)
	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("DS_ERROR"))).
		Return(nil).Run(func(_ mock.Arguments) { wg.Done() })
	updater.On("AdminDeleteBulkGameSessionsShort", mock.MatchedBy(deleteMatcher("sess-lost"))).
		Return(&sessionclientmodels.ApimodelsDeleteBulkGameSessionsAPIResponse{}, nil).
		Run(func(_ mock.Arguments) { wg.Done() })

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-lost", "ns")
	assert.NoError(t, registry.RegisterHost(scope, "ns", "sess-lost", "127.0.0.1", 7777, "host-1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go registry.StartHeartbeatMonitor(ctx)

	waitWithTimeout(t, &wg, 2*time.Second)
	updater.AssertExpectations(t)
}

func TestHostlessSession_DisabledWithZero(t *testing.T) {
	updater := new(MockSessionUpdater)
	cfg := testConfig()
	cfg.PlayerHostedHostlessSessionTimeout = 0
	registry := playerhosted.New(cfg, updater)

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-keep", "ns")
	defer registry.Release(scope, "sess-keep")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go registry.StartHeartbeatMonitor(ctx)

	time.Sleep(50 * time.Millisecond)
	updater.AssertNotCalled(t, "AdminDeleteBulkGameSessionsShort", mock.Anything)
}

func TestEmptySession_IsDeleted(t *testing.T) {
	updater := new(MockSessionUpdater)
	cfg := testConfig()
	cfg.PlayerHostedHeartbeatTimeout = time.Minute
	cfg.PlayerHostedEmptySessionTimeout = 20 * time.Millisecond
	registry := playerhosted.New(cfg, updater)

	var wg sync.WaitGroup
	wg.Add(1)
	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("AVAILABLE"))).Return(nil)
	updater.On("GetGameSessionShort", mock.Anything).Return(sessionWithMembers("LEFT", "DROPPED"), nil)
	updater.On("AdminDeleteBulkGameSessionsShort", mock.MatchedBy(deleteMatcher("sess-empty"))).
		Return(&sessionclientmodels.ApimodelsDeleteBulkGameSessionsAPIResponse{}, nil).
		Run(func(_ mock.Arguments) { wg.Done() })

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-empty", "ns")
	assert.NoError(t, registry.RegisterHost(scope, "ns", "sess-empty", "127.0.0.1", 7777, "host-1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go registry.StartHeartbeatMonitor(ctx)

	waitWithTimeout(t, &wg, 2*time.Second)
	assert.Error(t, registry.Heartbeat("sess-empty"))
	updater.AssertExpectations(t)
}

func TestEmptySession_ActiveMemberKeepsSession(t *testing.T) {
	updater := new(MockSessionUpdater)
	cfg := testConfig()
	cfg.PlayerHostedHeartbeatTimeout = time.Minute
	cfg.PlayerHostedEmptySessionTimeout = 10 * time.Millisecond
	registry := playerhosted.New(cfg, updater)

	updater.On("AdminUpdateDSInformationShort", mock.MatchedBy(statusMatcher("AVAILABLE"))).Return(nil)
	updater.On("GetGameSessionShort", mock.Anything).Return(sessionWithMembers("LEFT", "CONNECTED"), nil)

	scope := envelope.NewRootScope(context.Background(), "test", "")
	registry.AwaitRegistration(scope, "sess-busy", "ns")
	assert.NoError(t, registry.RegisterHost(scope, "ns", "sess-busy", "127.0.0.1", 7777, "host-1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go registry.StartHeartbeatMonitor(ctx)

	time.Sleep(60 * time.Millisecond)
	assert.NoError(t, registry.Heartbeat("sess-busy"))
	updater.AssertCalled(t, "GetGameSessionShort", mock.Anything)
	updater.AssertNotCalled(t, "AdminDeleteBulkGameSessionsShort", mock.Anything)
}
