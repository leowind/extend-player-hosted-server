# Player-hosted servers are now supported with AGS and Extend

Some multiplayer experiences work best when players can create a space of their own. Instead of joining a server managed entirely by the game, players can host a server, invite their friends, and decide how they want to play together.

This model has become increasingly common in multiplayer games, with games like Rust and Roblox giving players different ways to create and host their own experiences and communities.

With persistent sessions and a new Extend app template, you can now build player-hosted server experiences with AccelByte Gaming Services (AGS).

Players can host a game server from their own PC, invite friends to join, and keep the same session available across multiple matches. AGS manages the session while your Extend app handles the connection between the player-hosted server and the session.

## Connect player-hosted servers to AGS

The new Extend app template provides the integration needed to connect a player-hosted server with an AGS session.

A typical flow looks like this:

1. A player selects **Host a Server** and the game creates a persistent AGS session.
2. The player's PC starts the game server and registers its address with the Extend app.
3. The Extend app verifies that the player is part of the session and that the server is reachable.
4. The server address is reported to AGS, allowing other players to retrieve the connection information and join.

[GIF-1: Player hosts a server from their PC and another player joins from an invite]

The Extend app handles the AGS integration, while your game controls the player experience and the rules around hosting.

Under the hood, the session uses a session template with a custom dedicated server that points at your Extend app:

| Setting | Value |
|---|---|
| `dsSource` | `custom` |
| `persistent` | `true` |
| `asyncProcessDSRequest.async` | `true` (the host's address isn't known until it registers) |
| `asyncProcessDSRequest.timeout` | `1` to `600` seconds |

When the session needs a server, AGS asks the Extend app for one, and the app waits for the host to register. The host's game registers through the app's registration API, using the player's own access token:

```
POST /playerhosted/v1/namespaces/{namespace}/sessions/{sessionId}/register
Authorization: Bearer <player access token>
{ "ip": "203.0.113.10", "port": 7777, "serverId": "host-1" }
```

The app checks that the caller is the session leader or a member, opens a TCP connection to the address to make sure it is reachable, and then reports it to AGS. The session's server status becomes `AVAILABLE`, and players who join can read the address and connect.

## Keep sessions available across matches

Player-hosted servers can use persistent sessions that remain available as players join and leave and across multiple matches.

While the server is running, it sends a heartbeat through the Extend app. If the heartbeat stops, the app can mark the server as unavailable while the AGS session remains intact.

```
POST /playerhosted/v1/namespaces/{namespace}/sessions/{sessionId}/heartbeat
Authorization: Bearer <player access token>
```

In the template, a host that misses its heartbeat for 60 seconds is reported as unavailable (`DS_ERROR`). AGS keeps the session and its players, and asks the app for a server again, so the host, or another player, can register and pick up where they left off.

This also allows you to implement features such as host migration, where another player can take over as the host without creating a new session.

You can define when a persistent session should end, such as when the host ends it, when your last-player-leaves policy is triggered, or when an administrator closes the session.

Persistent sessions don't expire on their own, so in this model **your Extend app owns the session's lifecycle**. The template comes with two end policies that you can tune or turn off:

| Policy | Default | What happens |
|---|---|---|
| No host | 10 minutes | The session has had no live host (none registered, or the heartbeat was lost) for this long, so the app ends it. |
| No players | 30 minutes | The server is up but no player has been in the session for this long, so the app ends it. |

You can also end a session directly from your game or from the AGS Admin Portal at any time. Each policy is a single setting (`PLAYERHOSTED_HOSTLESS_SESSION_TIMEOUT` and `PLAYERHOSTED_EMPTY_SESSION_TIMEOUT`), and setting it to `0` turns it off.

[GIF-2: Host stops the server, the session shows the server as unavailable, and the host comes back and registers again]

## Start with an Extend app template

The new template provides the core functionality needed to register and monitor a player-hosted server, including:

- Receiving server registration requests
- Validating the host and session
- Monitoring the server heartbeat
- Reporting the server address to the AGS session
- Ending sessions that no longer have a host or players

You can customize the template to match your game's requirements, including:

- Who can host a server
- The maximum number of players
- How empty sessions are handled
- Additional validation and game-specific rules

This lets you focus on the parts of the hosting experience that are specific to your game while using AGS for session management.

The template also includes a small end-to-end demo that you can run against your own namespace:

| Demo | What it does |
|---|---|
| `demo-setup` | Creates the session template. |
| `demo-ds` | Acts as the host: creates a persistent session, starts a small game server, registers it and sends heartbeats. |
| `demo-client` | Acts as a second player: joins the session, waits for the server to be available and connects to it. |

```shell
go run ./cmd/demo-setup
go run ./cmd/demo-ds                               # prints SESSION_ID=...
go run ./cmd/demo-client --session <SESSION_ID>
```

Stop `demo-ds` to see the heartbeat and end policies in action.

[GIF-3: Running the demo: demo-ds registers, demo-client joins and connects]

## Use your own infrastructure

Player-hosted servers aren't limited to servers running on a player's PC.

You can also use the Extend app with your own VMs or bare-metal infrastructure. For example, your application can select a machine from your existing server pool, start a game server, and register it with the AGS session.

If a machine becomes unavailable, your application can detect the failure and provision a replacement server for the same session.

## Important considerations

There are a few things to keep in mind when building a player-hosted server experience.

- **Host IP addresses are visible.** Because players connect directly to the host, the host's public IP address is visible to other players in the session.
- **Hosts may need network configuration.** Players outside the host's local network may not be able to connect by default. The host may need to allow incoming connections through their firewall and configure port forwarding on their router. The Extend app checks that the address is reachable before it accepts the registration.
- **Player machines should be treated as untrusted.** Keep the registration checks enabled and don't provide hosts with administrative credentials. The Extend app should be responsible for validating and reporting the server address to AGS.
- **Persistent sessions don't automatically expire.** Define an appropriate end policy so inactive sessions don't accumulate indefinitely. The template's no-host and no-players policies are a starting point.
- **The registration API is public.** Hosts call the Extend app directly, so the app's registration endpoint needs to be reachable from the internet, in addition to the gRPC endpoint that AGS calls.

## Get started with player-hosted servers

With persistent sessions and the new Extend app template, you can now build player-hosted server experiences using AGS.

Whether you're building a game where players host directly from their PCs or integrating AGS with your own server infrastructure, you can use the same session-based approach to keep your servers and players connected.

Learn more about the Extend app template and get started with player-hosted servers in the AGS documentation.

- [LINK: Player-hosted Extend app template repository]
- [LINK: AGS documentation: Persistent sessions]
- [LINK: AGS documentation: Session DSM Extend Override]
