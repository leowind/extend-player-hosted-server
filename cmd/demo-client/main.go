// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

// Command demo-client is the joining-player side of the player-hosted demo
// (proposal Flow B). It authenticates as a joining player, joins the session the
// host created, waits for the DS to become AVAILABLE, then connects to the host's
// advertised address to prove the end-to-end flow.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"extend-player-hosted-server/cmd/internal/demokit"

	"github.com/AccelByte/accelbyte-go-sdk/session-sdk/pkg/sessionclient/game_session"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sessionID := flag.String("session", os.Getenv("DEMO_SESSION_ID"), "game session id printed by demo-ds (SESSION_ID=...)")
	flag.Parse()
	if *sessionID == "" {
		log.Fatal("demo-client: --session <id> (or DEMO_SESSION_ID) is required")
	}

	namespace, err := demokit.MustEnv("AB_NAMESPACE")
	if err != nil {
		log.Fatalf("demo-client: %v", err)
	}
	joinUser, err := demokit.MustEnv("DEMO_JOINER_USERNAME")
	if err != nil {
		log.Fatalf("demo-client: %v", err)
	}
	joinPass, err := demokit.MustEnv("DEMO_JOINER_PASSWORD")
	if err != nil {
		log.Fatalf("demo-client: %v", err)
	}
	pollInterval := demokit.EnvDuration("DEMO_POLL_INTERVAL", 3*time.Second)

	clients := demokit.NewClients()
	if err := clients.LoginUser(joinUser, joinPass); err != nil {
		log.Fatalf("demo-client: joiner login failed: %v", err)
	}

	// Join the session so this player becomes a member (and can read the session).
	if _, err := clients.Session.JoinGameSessionShort(&game_session.JoinGameSessionParams{
		Namespace: namespace,
		SessionID: *sessionID,
		Context:   ctx,
	}); err != nil {
		log.Fatalf("demo-client: join session %s: %v", *sessionID, err)
	}
	log.Printf("demo-client: joined session %s; waiting for DS...", *sessionID)

	ip, port, err := demokit.PollDSInfo(ctx, clients.Session, namespace, *sessionID, pollInterval)
	if err != nil {
		log.Fatalf("demo-client: waiting for DS: %v", err)
	}
	log.Printf("demo-client: DS AVAILABLE at %s:%d", ip, port)

	if err := connectAndPlay(ctx, ip, int(port)); err != nil {
		log.Fatalf("demo-client: connect to DS: %v", err)
	}
}

// connectAndPlay dials the dedicated server, exchanges a line, and prints what it
// receives — proving the joiner can reach the host-provided address.
func connectAndPlay(ctx context.Context, ip string, port int) error {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	welcome, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading welcome: %w", err)
	}
	log.Printf("demo-client: DS says: %s", trimNewline(welcome))

	if _, err := fmt.Fprintf(conn, "hello from demo-client\n"); err != nil {
		return fmt.Errorf("sending hello: %w", err)
	}
	echo, err := reader.ReadString('\n')
	if err == nil && echo != "" {
		log.Printf("demo-client: DS echoed: %s", trimNewline(echo))
	}

	log.Printf("demo-client: connected & playing on %s — end-to-end flow complete", addr)

	return nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}

	return s
}
