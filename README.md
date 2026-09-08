# WhatsApp‑MCP (Go Edition)

A lightweight, WhatsApp MCP server and bridge — rewritten entirely in Go for simplicity, portability, and real‑world automation workflows.

This project is a re‑imagining of the original **[whatsapp-mcp](https://github.com/lharries/whatsapp-mcp)** by **lharries**, who created the first WhatsApp MCP bridge using a Python MCP server and a Go WhatsApp client powered by **WhatsMeow**. Their work demonstrated how Claude Desktop could interact with WhatsApp through the MCP protocol, and this project would not exist without that foundation.

Start `whatsapp-bridge` -> then run `whatsapp-mcp-server` in your preferred mode (STDIO or HTTP).

| Connector | Chat |
|------------|----------|
| ![Connector](./screenshots/connector_claude.png) | ![Chat](./screenshots/chat_example.png) |

## Installation

### Prerequisites

- Go
- Anthropic Claude Desktop app (or Cursor) or `n8n` workflow
- FFmpeg (_optional_) - Only needed for audio messages. If you want to send audio files as playable WhatsApp voice messages, they must be in `.ogg` Opus format. With FFmpeg installed, the MCP server will automatically convert non-Opus audio files. Without FFmpeg, you can still send raw audio files using the `send_file` tool.

### Steps
1. **Clone this repository**

   ```bash
   git clone https://github.com/iamatulsingh/whatsapp-mcp-go.git
   cd whatsapp-mcp-go
   ```

2. **Run the WhatsApp bridge**
   Navigate to the whatsapp-bridge directory and run the Go application:

   ```bash
   cd whatsapp-bridge
   go run main.go
   ```

   The first time you run it, you will be prompted to scan a QR code. Scan the QR code with your WhatsApp mobile app to authenticate.
   After approximately 20 days, you will might need to re-authenticate.

   3. **Connect to the MCP server**
       ```bash
       cd whatsapp-mcp-server
       go build -o whatsapp-mcp
       ```
      Copy the below json with the appropriate {{PROJECT_BASE_PATH}} value:
      ```json
      {
           "mcpServers": {
               "whatsapp-mcp": {
                  "command": "{{PROJECT_BASE_PATH}}/whatsapp-mcp-server/whatsapp-mcp",
                  "env": {
                       "WHATSAPP_API_KEY": "c3VwZXItbG9uZy1yYW5kb20tc3RyaW5nLW1pbmltdW0tb2YtNjQtY2hhcmFjdGVycy15b3UtbmVlZC10by1wYXN0ZS1oZXJl"
                  }
               }
           },
           "preferences": {
               "sidebarMode": "chat",
               "coworkScheduledTasksEnabled": false
           }
       }
      ```

      For **Claude**, save this as `claude_desktop_config.json` in your Claude Desktop configuration directory at:

      ```
      ~/Library/Application Support/Claude/claude_desktop_config.json
      ```

      For **Cursor**, save this as `mcp.json` in your Cursor configuration directory at:

      ```
      ~/.cursor/mcp.json
      ```

### Windows Compatibility

If you're running this project on Windows, be aware that `go-sqlite3` requires **CGO to be enabled** in order to compile and work properly. By default, **CGO is disabled on Windows**, so you need to explicitly enable it and have a C compiler installed.

#### Steps to get it working:

1. **Install a C compiler**  
   We recommend using [MSYS2](https://www.msys2.org/) to install a C compiler for Windows. After installing MSYS2, make sure to add the `ucrt64\bin` folder to your `PATH`.  
   → A step-by-step guide is available [here](https://code.visualstudio.com/docs/cpp/config-mingw).

2. **Enable CGO and run the app**

   ```bash
   cd whatsapp-bridge
   go env -w CGO_ENABLED=1
   go run main.go # or use this to enabled webhook and http streaming, `WEBHOOK_URL=http://192.168.178.119:5777/sse IS_HTTP=true go run main.go`
   ```

### Or run everything in Docker

The repo ships a `docker-compose.yaml` at the root that brings up three
services: `postgres`, `wa-bridge`, and `wa-mcp`. The MCP server is
started in **HTTP mode** (port 5777) because that is the only mode that
fits a long-running container — see "MCP server: stdio vs HTTP" below
for when to use each.

```bash
# 1. Set the four required vars (in .env at repo root, or in your shell)
cat > .env <<EOF
WHATSAPP_API_KEY=$(openssl rand -base64 48)
WHATSAPP_JWT_SECRET=$(openssl rand -base64 48)
POSTGRES_USER=whatsapp
POSTGRES_PASS=$(openssl rand -base64 24)
EOF

# 2. Bring it all up
docker compose up
```

Once running:
- Bridge REST API: `http://localhost:8080/api/...` (after `/auth/login`)
- MCP HTTP endpoint: `http://localhost:5777`

### MCP server: stdio vs HTTP

The MCP server has two run modes selected by `IS_HTTP`:

| Mode | When to use | How to launch |
| --- | --- | --- |
| **stdio** (`IS_HTTP=false`, default) | Claude Desktop, Cursor, or any host that spawns the MCP server as a child process | `go build` a local binary; reference it from `claude_desktop_config.json` / `mcp.json` (see below) |
| **HTTP** (`IS_HTTP=true`) | n8n, web automation, or any client connecting over the network | `docker compose up` — the `wa-mcp` service runs in HTTP mode by default, listening on `:5777` |

Stdio mode is **not** appropriate for the Docker image — Claude Desktop
does not natively `docker run` to spawn an MCP child. Build a local
binary instead.

## Authentication

The HTTP API is protected by a two-step **API key → JWT** flow.

1. Send your static API key to `/auth/login` once. The bridge returns a 45-minute JWT.
2. Use the JWT as `Authorization: Bearer <jwt>` on every `/api/...` call.

### Step 1: Get a JWT

```bash
curl -X POST \
  -H "Authorization: Bearer $WHATSAPP_API_KEY" \
  http://localhost:8080/auth/login
# {"token":"eyJhbGciOiJIUzI1NiIs..."}
```

### Step 2: Call the API

```bash
TOKEN=eyJhbGciOiJIUzI1NiIs...
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/chats
```

The MCP server does this automatically: configure `WHATSAPP_API_KEY` and it
fetches/refreshes JWTs as needed.

### Rate limiting

`/auth/login` is rate-limited per client IP (default: 5 attempts / minute).
Override with `AUTH_LOGIN_RATE=<count>/<window>` (e.g. `10/30s`). Behind a
reverse proxy, terminate rate limiting upstream — the bridge currently uses
`r.RemoteAddr` and does not consult `X-Forwarded-For`.

### Media path restrictions

`POST /api/send` reads `media_path` from the **bridge's** filesystem. To keep
the API from being usable for reading arbitrary host files, paths are only
accepted inside the bridge's `store/` directory or the OS temp directory by
default. Override the allowlist with `MEDIA_ALLOWED_DIRS` (colon-separated),
e.g. `MEDIA_ALLOWED_DIRS=/data/media:/shared`.

### Postgres TLS

The bridge connects with `sslmode=disable` by default (matching the bundled
docker-compose network). Set `POSTGRES_SSLMODE` (e.g. `require`,
`verify-full`) when the database is reached over an untrusted network.

## The bridge contract (`/bridge/v1`)

Alongside `/api`, the bridge exposes a second, separately-authenticated
surface for the Mindet obligation ledger to read
messages, chats and contacts and to send and log in — see the platform spec
(`docs/superpowers/specs/2026-09-07-platform-design.md` in the Mindet
repository) §3 for the full contract. **Mindet is the only intended caller;
the MCP server keeps `/api`.**

| Method | Path | Capability | Meaning |
| --- | --- | --- | --- |
| `GET` | `/bridge/v1/health` | — | Connection/auth state, capabilities, `since` |
| `GET` | `/bridge/v1/messages` | `messages` | Arrival-ordered page of messages (cursor, `until`) |
| `GET` | `/bridge/v1/chats` | `chats` | Direct/group/feed chats known to the bridge |
| `GET` | `/bridge/v1/contacts` | `contacts` | Address-book contacts with lid/phone aliases |
| `GET` | `/bridge/v1/media/{id}` | `media` | Fetch (idempotently) and describe one message's file |
| `POST` | `/bridge/v1/send` | `send` | Send a message, idempotent on `idempotency_key` |
| `POST` | `/bridge/v1/login` | `login` | Drive the QR/pairing-code login flow |

Three settings gate and shape it:

| Var | Meaning |
| --- | --- |
| `MINDET_BRIDGE_TOKEN` | The one static bearer that opens `/bridge/v1`. Empty disables the whole surface; it is a separate door from the `/api` JWT, so neither caller can use the other's credential. ≥32 chars: `openssl rand -base64 48`. |
| `MEDIA_SHARED_ROOT` | The mount both this bridge and Mindet's daemon can see (default `/shared`). A contract message's `files[].path` is relative to it. |
| `MEDIA_DOWNLOAD_DIR` | Where `/bridge/v1/media` (and `/api/download`) write decrypted media (default `store`, or `/shared/whatsapp` under the bundled compose file). When `MINDET_BRIDGE_TOKEN` is set this **must** resolve inside `MEDIA_SHARED_ROOT` — startup refuses to come up otherwise, because every `files[].path` the contract hands back would be wrong. |

The QR-link page at `BRIDGE_PUBLIC_URL/bridge/v1/qr/<token>` is
unauthenticated by design: the token itself is the secret, and it expires
after ten minutes, so no bearer is needed or checked on that route.

### Deploying with Mindet

`BRIDGE_PUBLIC_URL` has no safe default once `MINDET_BRIDGE_TOKEN` is set:
the login QR link the daemon relays to the owner is built from it, and a
`localhost` URL only resolves inside the container. Startup now refuses that
combination — with the token set, `BRIDGE_PUBLIC_URL` must be present and
must not be `localhost`/`127.0.0.1` — so a dead login link cannot ship
quietly. (docker-compose can't express "required only if X is set", so the
bundled `docker-compose.yaml` still carries a localhost default; the bridge
is the enforcement.)

**Before the first start against a real mirror**, check how much there is to
migrate:

```sql
SELECT count(*) FROM messages WHERE arrival_seq IS NULL;
```

The first start of this version gives every existing row an arrival order,
in batches of 50 000, and does not answer HTTP — `/api` included — until it
finishes; it logs the row count before it starts and the elapsed time as it
goes. Expect the first start to take proportionally longer on a mirror with
years of history, and read the log rather than assuming the container hung.
Later starts skip it entirely (the column is only filled where it is NULL).

Media already downloaded under the old private `store/` path would be
reported `pending` forever once `MEDIA_DOWNLOAD_DIR` moves, since the bridge
looks for the file under the new dir. The owner's stack already downloads to
`/shared/whatsapp`, which is exactly the value this compose file defaults to,
so nothing is orphaned there; on any other deployment, move or symlink the
old directory before the first start.

For the owner's own stack (`/home/askar/stack/compose/personal.yml`, not in
this repository — an operator step, not a code change), add to
`whatsapp-bridge.environment`:

```yaml
      MINDET_BRIDGE_TOKEN: ${MINDET_BRIDGE_TOKEN_WHATSAPP:?MINDET_BRIDGE_TOKEN_WHATSAPP must be set in .env}
      MEDIA_SHARED_ROOT: /shared
      BRIDGE_PUBLIC_URL: ${WHATSAPP_BRIDGE_PUBLIC_URL:-http://localhost:8080}
```

Mindet's own `docs/deploy/compose.mindet.yml` already reads
`MINDET_BRIDGE_TOKEN_WHATSAPP`, so one value in the stack's `.env` serves
both sides. `WHATSAPP_BRIDGE_PUBLIC_URL` is the stack-wide name for the URL
the owner's browser reaches this bridge at (it is not read by the bridge
itself — only `BRIDGE_PUBLIC_URL` is).

### Verification: Mindet's conformance suite against this build

Mindet ships a pytest suite (`tests/contract/`) that is the acceptance test
for `/bridge/v1` — the same suite runs against Mindet's own fake and, with
`MINDET_CONTRACT_URL`/`MINDET_CONTRACT_TOKEN` pointed at a live bridge,
against this one. Four of its nine tests seed a store directly and are
skipped against a real bridge (they are seeding tests; `TestEndToEndArrivalOrderEditsAndMedia`
in `whatsapp-bridge/contract_e2e_test.go` covers the same ground as a Go
test against a real store instead). Run:

The bridge under test needs `BRIDGE_PUBLIC_URL` set to something that is not
localhost (it is only ever used to build the login QR link, so any hostname
the owner could reach will do); the suite itself still talks to it on
whatever address you give `MINDET_CONTRACT_URL`.

```bash
cd /home/askar/src/mindet
# MINDET_CONTRACT_URL is the bridge's own base URL, WITHOUT /bridge/v1 —
# BridgeClient appends that suffix itself (mindet/contract/client.py); passing
# it here would ask the bridge for /bridge/v1/bridge/v1/... and 404 on
# everything.
MINDET_CONTRACT_URL=http://127.0.0.1:18080 \
MINDET_CONTRACT_TOKEN=<the bridge's MINDET_BRIDGE_TOKEN> \
uv run pytest tests/contract -q
```

Last verified against bridge commit `77f8cdc6cbc013e49ec22267215616f0a531b5f3`
(the code this task's docs/config/test commit sits on top of — the contract
endpoints under test are unchanged by that commit):
- SQLite: `5 passed, 4 skipped in 0.32s`
- Postgres 16 (`IS_POSTGRES=true`, against a throwaway `postgres:16`
  container; the schema this bridge migrates on startup —
  `ensureContractColumns` adds the arrival/kind/edits columns, the
  `messages_arrival_seq` sequence, and backfills them — was confirmed with
  `\d messages` afterward): `5 passed, 4 skipped in 0.31s`

The bridge does not need a paired WhatsApp session for this: the HTTP server
comes up before the client connects, and even a failed fetch of the current
WhatsApp Web client version (no network) only logs and does not stop
startup (`CustomGetLatestVersion`, called from `main()`). In this run the
fetch actually succeeded (outbound network was available), so that path
was not exercised live — it is confirmed by reading `main.go` instead (the
error from `CustomGetLatestVersion` is logged and the function falls
through to starting the HTTP server regardless).

## Architecture Overview

This application consists of two main components:

1. **WhatsApp Bridge** (`whatsapp-bridge/`): A Go application that connects to WhatsApp's web API, handles authentication via QR code, and stores message history in SQLite. It serves as the bridge between WhatsApp and the MCP server.

2. **MCP Server** (`whatsapp-mcp-server/`): A Go implemention of the Model Context Protocol (MCP), which provides standardized tools for Claude to interact with WhatsApp data and send/receive messages.

### Data Storage

- All message history is stored in `postgres` by default and you'll need to create a database name `whatsapp` OR a SQLite database within the `whatsapp-bridge/store/` directory
- The database maintains tables for chats and messages
- Messages are indexed for efficient searching and retrieval

### MCP Tools

Claude can access the following tools to interact with WhatsApp:

- **search_contacts**: Search for contacts by name or phone number
- **list_messages**: Retrieve messages with optional filters and context
- **list_chats**: List available chats with metadata
- **get_chat**: Get information about a specific chat
- **get_direct_chat_by_contact**: Find a direct chat with a specific contact
- **get_contact_chats**: List all chats involving a specific contact
- **get_last_interaction**: Get the most recent message with a contact
- **get_message_context**: Retrieve context around a specific message
- **send_message**: Send a WhatsApp message to a specified phone number or group JID
- **send_file**: Send a file (image, video, raw audio, document) to a specified recipient
- **send_audio_message**: Send an audio file as a WhatsApp voice message (requires the file to be an .ogg opus file or ffmpeg must be installed)
- **download_media**: Download media from a WhatsApp message and get the local file path
- **get_login_status**: Check whether the bridge is connected and logged in to WhatsApp. Returns `{connected, logged_in, pairing_required}`.
- **get_pairing_qr**: Fetch the WhatsApp pairing QR as a PNG image. Returns image content when pairing is required, or a text message when the bridge is already logged in. Useful for completing the initial device-link flow from inside an MCP-aware UI instead of from the terminal.

### Media Handling Features

The MCP server supports both sending and receiving various media types:

#### Media Sending

You can send various media types to your WhatsApp contacts:

- **Images, Videos, Documents**: Use the `send_file` tool to share any supported media type.
- **Voice Messages**: Use the `send_audio_message` tool to send audio files as playable WhatsApp voice messages.
  - For optimal compatibility, audio files should be in `.ogg` Opus format.
  - With FFmpeg installed, the system will automatically convert other audio formats (MP3, WAV, etc.) to the required format.
  - Without FFmpeg, you can still send raw audio files using the `send_file` tool, but they won't appear as playable voice messages.

#### Media Downloading

By default, just the metadata of the media is stored in the local database. The message will indicate that media was sent. To access this media you need to use the download_media tool which takes the `message_id` and `chat_jid` (which are shown when printing messages containing the meda), this downloads the media and then returns the file path which can be then opened or passed to another tool.


## Technical Details

1. Claude sends requests to the MCP server
2. The MCP server queries the Go bridge for WhatsApp data or directly to the SQLite database
3. The Bridge accesses the WhatsApp API and keeps the SQLite or Postgres database up to date
4. Data flows back through the chain to Claude
5. When sending messages, the request flows from Claude through the MCP server to the bridge and to WhatsApp

## Troubleshooting

- Make sure both the Bridge application and the MCP server are running for the integration to work properly.
- In case of the Client outdated (405) error in the Bridge Go server,
    ```bash
    06:13:28.434 [Client INFO] Starting WhatsApp client...
    2025/07/29 06:13:28 Connecting to postgres
    06:13:30.402 [Client ERROR] Client outdated (405) connect failure (client version: 2.3000.1021018791)
    06:13:30.403 [Client/Socket ERROR] Error reading from websocket: websocket: close 1006 (abnormal closure): unexpecte
    06:13:30.675 [Client ERROR] Failed to establish stable connection
    ```

    Run below command and fix any error in your code after upgrade and the bridge code will automatic update the whatsapp client version.
    
    ```bash
    go get -u go.mau.fi/whatsmeow@latest
    ```
    
    OR Update all packages
    
    ```bash
    go get -u
    ```

### Authentication Issues

- **QR Code Not Displaying**: If the QR code doesn't appear, try restarting the authentication script. If issues persist, check if your terminal supports displaying QR codes.
- **WhatsApp Already Logged In**: If your session is already active, the Go bridge will automatically reconnect without showing a QR code.
- **Device Limit Reached**: WhatsApp limits the number of linked devices. If you reach this limit, you'll need to remove an existing device from WhatsApp on your phone (Settings > Linked Devices).
- **No Messages Loading**: After initial authentication, it can take several minutes for your message history to load, especially if you have many chats.
- **WhatsApp Out of Sync**: If your WhatsApp messages get out of sync with the bridge, delete both database files (`whatsapp-bridge/store/messages.db` and `whatsapp-bridge/store/whatsapp.db`) and restart the bridge to re-authenticate.

This fork takes the core idea and rebuilds it with a different philosophy:  
**one language, one binary, clean architecture, and flexible deployment.**

---

## Why This Project Exists

The original project is excellent, but it was designed specifically for **Claude Desktop**, and its architecture had a few constraints that made extending or deploying it more difficult. This fork was created to solve those pain points.

* ***Single Language: Everything in Go**
The original design split responsibilities: This dramatically simplifies development and distribution.

* **Clean Architecture: No Mixed Database Logic** 
Moving all database logic into the bridge

* **SQLite or PostgreSQL — Your Choice**
The original project only supported SQLite.

* **Two Communication Modes: STDIO + HTTP** The original project was built only for Claude Desktop (STDIO MCP). This makes the project usable far beyond Claude Desktop.

* **Docker Support** This makes deployment trivial on:
- servers  
- containers  
- orchestrators  
- local development  

---

## Features

- Full WhatsApp client using **WhatsMeow**
- Pure Go MCP server
- Clean API boundary between MCP and bridge
- SQLite or PostgreSQL support
- STDIO + HTTP modes
- Lightweight Docker image
- Easy deployment with Docker Compose
- No Python dependencies
- No mixed database logic
- Production‑ready architecture

---

## Credits

This project stands on the shoulders of two major contributors:

### **Original Author: lharries**
The initial idea, architecture, and implementation of WhatsApp‑MCP came from  
**https://github.com/lharries/whatsapp-mcp**

Their work demonstrated how MCP could be used to bridge WhatsApp and Claude Desktop.  
This fork would not exist without their project.

### **WhatsMeow**
The WhatsApp client functionality is powered by the incredible Go library:  
**https://github.com/tulir/whatsmeow**

Without WhatsMeow, none of this would be possible.

---

## Why You Might Use This Fork

Choose this version if you want:

- a cleaner architecture  
- a single‑language codebase  
- a portable binary  
- Docker support  
- PostgreSQL support  
- HTTP support for automation workflows  
- a more maintainable and extensible project  

The goal is not to replace the original project but to offer an alternative that fits different needs — especially for developers who prefer Go or want to deploy MCP‑based WhatsApp automation in production environments.

