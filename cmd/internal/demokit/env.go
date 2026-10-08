// Copyright (c) 2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

package demokit

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Env returns the value of key or def when unset/empty.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// MustEnv returns the value of key or an error when it is unset/empty.
func MustEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}

	return v, nil
}

// EnvInt returns key parsed as an int, or def when unset/unparseable.
func EnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}

	return def
}

// EnvBool returns key parsed as a bool, or def when unset/unparseable.
func EnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}

	return def
}

// EnvDuration returns key parsed as a time.Duration, or def when unset/unparseable.
func EnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}

	return def
}

// TemplateSpecFromEnv builds a TemplateSpec from the DEMO_* environment.
func TemplateSpecFromEnv() TemplateSpec {
	return TemplateSpec{
		Name:          Env("DEMO_CONFIG_NAME", "player-hosted-demo"),
		MaxPlayers:    EnvInt("DEMO_MAX_PLAYERS", 10),
		ClientVersion: Env("DEMO_CLIENT_VERSION", "1.0.0"),
		Deployment:    Env("DEMO_DEPLOYMENT", "player-hosted"),
		AppName:       os.Getenv("DEMO_APP_NAME"),
		CustomURLGRPC: os.Getenv("DEMO_CUSTOM_URL_GRPC"),
		AsyncTimeout:  EnvInt("DEMO_ASYNC_TIMEOUT", 120),
		Persistent:    EnvBool("DEMO_PERSISTENT", true),
	}
}
