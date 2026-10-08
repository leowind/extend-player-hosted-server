// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

// Command demo-ds is the host side of the player-hosted demo (proposal Flow B).
// It authenticates as the host player, creates a persistent custom-DS session
// (becoming its leader), starts a trivial TCP "game server", registers that
// address with the Extend player-hosted app, and then heartbeats until interrupted.
//
// It prints SESSION_ID=<id> on startup; pass that to demo-client to join and
// connect.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"extend-player-hosted-server/cmd/internal/demokit"

	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclient/game_session"
	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclientmodels"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("demo-ds: %v", err)
	}

	clients := demokit.NewClients()

	// Best-effort: make sure the session template exists. Requires an admin
	// client; if that is not this credential, run `demo-setup` separately.
	ensureTemplate(ctx, clients, cfg.namespace)

	// Authenticate as the host player so the session leader (and thus the
	// registration caller) is a real session member.
	if err := clients.LoginUser(cfg.hostUser, cfg.hostPass); err != nil {
		log.Fatalf("demo-ds: host login failed: %v", err)
	}

	// Start the local game server before creating the session: the Extend app
	// TCP-probes the reported address during registration.
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.dsPort))
	if err != nil {
		log.Fatalf("demo-ds: listen on :%d: %v", cfg.dsPort, err)
	}
	defer func() { _ = listener.Close() }()
	go serveGame(listener, cfg.serverID)
	log.Printf("demo-ds: game server listening on :%d (advertising %s:%d)", cfg.dsPort, cfg.dsIP, cfg.dsPort)

	// Create the persistent custom-DS session; the host becomes its leader.
	cfgName := cfg.configName
	created, err := clients.Session.CreateGameSessionShort(&game_session.CreateGameSessionParams{
		Namespace: cfg.namespace,
		Body:      &sessionclientmodels.ApimodelsCreateGameSessionRequest{ConfigurationName: &cfgName},
		Context:   ctx,
	})
	if err != nil {
		log.Fatalf("demo-ds: create game session (configuration %q): %v", cfg.configName, err)
	}
	if created.ID == nil {
		log.Fatal("demo-ds: create game session returned no id")
	}
	sessionID := *created.ID
	log.Printf("SESSION_ID=%s", sessionID)

	token, err := clients.AccessToken()
	if err != nil {
		log.Fatalf("demo-ds: %v", err)
	}

	// Register the host address, retrying while AGS has not yet issued the async
	// DS request (the Extend app answers 409 until the session is awaiting a host).
	if err := registerWithRetry(ctx, cfg, sessionID, token); err != nil {
		log.Fatalf("demo-ds: registration failed: %v", err)
	}
	log.Printf("demo-ds: registered %s:%d for session %s — session should now be AVAILABLE", cfg.dsIP, cfg.dsPort, sessionID)

	heartbeatLoop(ctx, cfg, sessionID, token)
	log.Print("demo-ds: shutting down")
}

// dsConfig holds the resolved runtime configuration for the host.
type dsConfig struct {
	namespace         string
	hostUser          string
	hostPass          string
	configName        string
	regURL            string
	dsIP              string
	dsPort            int
	serverID          string
	heartbeatInterval time.Duration
}

func loadConfig() (dsConfig, error) {
	var cfg dsConfig
	var err error

	if cfg.namespace, err = demokit.MustEnv("AB_NAMESPACE"); err != nil {
		return cfg, err
	}
	if cfg.hostUser, err = demokit.MustEnv("DEMO_HOST_USERNAME"); err != nil {
		return cfg, err
	}
	if cfg.hostPass, err = demokit.MustEnv("DEMO_HOST_PASSWORD"); err != nil {
		return cfg, err
	}

	cfg.configName = demokit.Env("DEMO_CONFIG_NAME", "player-hosted-demo")
	cfg.regURL = demokit.Env("DEMO_REG_URL", "http://localhost:8081")
	cfg.dsIP = demokit.Env("DEMO_DS_IP", "127.0.0.1")
	cfg.dsPort = demokit.EnvInt("DEMO_DS_PORT", 7777)
	cfg.serverID = demokit.Env("DEMO_SERVER_ID", "demo-host-1")
	cfg.heartbeatInterval = demokit.EnvDuration("DEMO_HEARTBEAT_INTERVAL", 15*time.Second)

	return cfg, nil
}

// ensureTemplate tries to provision the session template using an admin client.
// Failures are non-fatal — the user can run demo-setup with an admin credential.
func ensureTemplate(ctx context.Context, clients *demokit.Clients, namespace string) {
	spec := demokit.TemplateSpecFromEnv()
	if spec.AppName == "" && spec.CustomURLGRPC == "" {
		log.Print("demo-ds: DEMO_APP_NAME/DEMO_CUSTOM_URL_GRPC unset; skipping template check (template must already exist)")

		return
	}
	if err := clients.LoginClient(); err != nil {
		log.Printf("demo-ds: skipping template ensure (client login failed: %v); run demo-setup separately if needed", err)

		return
	}
	token, err := clients.AccessToken()
	if err != nil {
		log.Printf("demo-ds: skipping template ensure (%v)", err)

		return
	}
	created, err := demokit.EnsureTemplate(ctx, clients.BaseURL(), namespace, token, spec)
	if err != nil {
		log.Printf("demo-ds: template ensure failed (%v); run demo-setup with an admin client if the template is missing", err)

		return
	}
	if created {
		log.Printf("demo-ds: created session configuration template %q", spec.Name)
	}
}

// serveGame is a stand-in dedicated server: it greets each connection and echoes
// one line back, which is enough to satisfy the reachability probe and let the
// joining client confirm a real connection.
func serveGame(listener net.Listener, serverID string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return // listener closed
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			_, _ = fmt.Fprintf(c, "WELCOME to player-hosted demo DS %s\n", serverID)
			line, _ := bufio.NewReader(c).ReadString('\n')
			if line != "" {
				_, _ = fmt.Fprintf(c, "ECHO %s", line)
			}
		}(conn)
	}
}

// registerWithRetry posts the host address until the session is awaiting
// registration (past the 409 window), backing off between attempts.
func registerWithRetry(ctx context.Context, cfg dsConfig, sessionID, token string) error {
	const maxWait = 2 * time.Minute
	deadline := time.Now().Add(maxWait)
	backoff := time.Second

	for {
		status, err := demokit.Register(ctx, cfg.regURL, cfg.namespace, sessionID, token, cfg.dsIP, cfg.dsPort, cfg.serverID)
		if err == nil {
			return nil
		}

		// 409 = session not yet awaiting registration (AGS hasn't dialled the
		// Extend app yet); status 0 = transport error. Both are retryable.
		retryable := status == 409 || status == 0
		if !retryable {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gave up after %s: %w", maxWait, err)
		}

		log.Printf("demo-ds: registration not ready yet (%v); retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

// heartbeatLoop keeps the host marked alive until the context is cancelled.
func heartbeatLoop(ctx context.Context, cfg dsConfig, sessionID, token string) {
	ticker := time.NewTicker(cfg.heartbeatInterval)
	defer ticker.Stop()

	log.Printf("demo-ds: heartbeating every %s (Ctrl-C to stop)", cfg.heartbeatInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := demokit.Heartbeat(ctx, cfg.regURL, cfg.namespace, sessionID, token); err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				log.Printf("demo-ds: heartbeat error: %v", err)

				continue
			}
			log.Printf("demo-ds: heartbeat ok (%s)", strconv.Quote(sessionID))
		}
	}
}
