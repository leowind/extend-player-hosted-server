// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

// Command demo-setup ensures the persistent, custom-DS, async session
// configuration template required by the player-hosted demo exists in the target
// namespace, creating it if necessary. It authenticates as the confidential
// client (AB_CLIENT_ID/AB_CLIENT_SECRET), which must hold admin permission to
// manage session configuration templates.
package main

import (
	"context"
	"log"

	"extend-player-hosted-server/cmd/internal/demokit"
)

func main() {
	ctx := context.Background()

	namespace, err := demokit.MustEnv("AB_NAMESPACE")
	if err != nil {
		log.Fatalf("demo-setup: %v", err)
	}

	spec := demokit.TemplateSpecFromEnv()
	if spec.AppName == "" && spec.CustomURLGRPC == "" {
		log.Fatal("demo-setup: set DEMO_APP_NAME (deployed Extend app) or DEMO_CUSTOM_URL_GRPC (direct gRPC URL) so AGS can reach the Extend player-hosted app")
	}

	clients := demokit.NewClients()
	if err := clients.LoginClient(); err != nil {
		log.Fatalf("demo-setup: client login failed: %v", err)
	}
	token, err := clients.AccessToken()
	if err != nil {
		log.Fatalf("demo-setup: %v", err)
	}

	created, err := demokit.EnsureTemplate(ctx, clients.BaseURL(), namespace, token, spec)
	if err != nil {
		log.Fatalf("demo-setup: ensure template %q: %v", spec.Name, err)
	}

	if created {
		log.Printf("demo-setup: created session configuration template %q (type=DS, persistent=true, dsSource=custom, async=true)", spec.Name)
	} else {
		log.Printf("demo-setup: session configuration template %q already exists; nothing to do", spec.Name)
	}
}
