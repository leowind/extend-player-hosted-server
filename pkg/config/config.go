// Copyright (c) 2018-2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

package config

import (
	"fmt"
	"reflect"
	"time"
)

// Config specifies configurable options through env vars
//
//nolint:lll
type Config struct {
	// Server Config
	GRPCPort                    int  `env:"GRPC_PORT" envDocs:"The Port gRPC listens to" envDefault:"6565"`
	PluginGRPCServerAuthEnabled bool `env:"PLUGIN_GRPC_SERVER_AUTH_ENABLED" envDocs:"Enable or disable access token and permission verification" envDefault:""`
	// AB Config
	ABBaseURL      string `env:"AB_BASE_URL" envDocs:"Base URL of AccelByte Gaming Services" envDefault:""`
	ABClientId     string `env:"AB_CLIENT_ID" envDocs:"Client ID from the Prerequisites section" envDefault:""`
	ABClientSecret string `env:"AB_CLIENT_SECRET" envDocs:"Client Secret from the Prerequisites section" envDefault:""`
	// Player-hosted Config
	PlayerHostedRegPort             int           `env:"PLAYERHOSTED_REG_PORT" envDocs:"Port for the player-hosted registration HTTP server" envDefault:"8081"`
	PlayerHostedRegTimeout          time.Duration `env:"PLAYERHOSTED_REG_TIMEOUT" envDocs:"How long to wait for a host to register before failing the DS request" envDefault:"120s"`
	PlayerHostedHeartbeatTimeout    time.Duration `env:"PLAYERHOSTED_HEARTBEAT_TIMEOUT" envDocs:"Mark the host as errored if no heartbeat is received within this window" envDefault:"60s"`
	PlayerHostedHeartbeatInterval   time.Duration `env:"PLAYERHOSTED_HEARTBEAT_INTERVAL" envDocs:"How often the heartbeat monitor checks for stale hosts" envDefault:"15s"`
	PlayerHostedReachabilityTimeout time.Duration `env:"PLAYERHOSTED_REACHABILITY_TIMEOUT" envDocs:"TCP dial timeout when probing a registered host address" envDefault:"5s"`
	// Persistent-session lifecycle (0 disables a policy)
	PlayerHostedHostlessSessionTimeout time.Duration `env:"PLAYERHOSTED_HOSTLESS_SESSION_TIMEOUT" envDocs:"Delete the AGS session after it has had no live host for this long (0 disables)" envDefault:"10m"`
	PlayerHostedEmptySessionTimeout    time.Duration `env:"PLAYERHOSTED_EMPTY_SESSION_TIMEOUT" envDocs:"Delete an AVAILABLE AGS session after it has had no JOINED/CONNECTED members for this long (0 disables)" envDefault:"30m"`
}

// HelpDocs returns documentation of Config based on field tags.
func (envVar Config) HelpDocs() []string {
	environmentVariables := envVar.EnvironmentVariables(nil)
	doc := make([]string, 1+len(environmentVariables))
	doc[0] = "Environment variables config:"
	for i := 1; i <= len(environmentVariables); i++ {
		doc[i+1] = fmt.Sprintf("  %v\t %v (default: %v)", environmentVariables[i].Name, environmentVariables[i].Description, environmentVariables[i].DefaultValue)
	}

	return doc
}

// EnvironmentVariables method to get a list of environment variables.
func (envVar Config) EnvironmentVariables(exposedVariables map[string]bool) []EnvironmentVariable {
	environmentVariables := make([]EnvironmentVariable, 0)
	reflectValue := reflect.ValueOf(envVar)
	reflectType := reflectValue.Type()

	for i := 0; i < reflectValue.NumField(); i++ {
		environmentVariable := newEnvironmentVariable(reflectValue, reflectType, i)
		if exposedVariables != nil {
			if _, ok := exposedVariables[environmentVariable.Name]; !ok {
				continue
			}
		}

		environmentVariables = append(environmentVariables, environmentVariable)
	}

	return environmentVariables
}

// EnvironmentVariable struct which contains env tags in config field.
type EnvironmentVariable struct {
	Name         string
	Description  string
	DefaultValue string
	ActualValue  string
}

func newEnvironmentVariable(reflectValue reflect.Value, reflectType reflect.Type, index int) EnvironmentVariable {
	field := reflectType.Field(index)

	return EnvironmentVariable{
		Name:         field.Tag.Get("env"),
		Description:  field.Tag.Get("envDocs"),
		DefaultValue: field.Tag.Get("envDefault"),
		ActualValue:  fmt.Sprintf("%v", reflectValue.Field(index).Interface()),
	}
}
