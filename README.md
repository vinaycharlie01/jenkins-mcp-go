# jenkins-mcp-go

A read-only [MCP](https://modelcontextprotocol.io) server, written in Go, that lets AI clients such as Claude Desktop and VS Code inspect **Jenkins**. Every call runs with **your own Jenkins credentials**, so Jenkins' authorization decides what you can see. It can be scoped to a single folder.

## Tools

| Tool | Jenkins API | What it returns |
|---|---|---|
| `jenkins_whoami` | `/whoAmI/api/json` | Who Jenkins thinks you are. Use it to check that the session works |
| `jenkins_list_jobs` | `…/api/json?tree=jobs[…]` | Jobs and folders in the base folder or a sub-folder: kind, status, last build. Filter: `search` |
| `jenkins_get_job_info` | `…/job/…/api/json` | Kind, status, health, last / last successful / last failed build, **parameter definitions**, child jobs |
| `jenkins_get_build_info` | `…/{build}/api/json` | Result, duration, **parameters**, who started it, Git commit/branch, changes. `build` = number or `lastBuild`, `lastFailedBuild`, … |
| `jenkins_get_build_history` | `…/api/json?tree=builds[…]{0,N}` | Recent builds, newest first |
| `jenkins_get_console_logs` | `…/{build}/logText/progressiveText` | Console output. **The default is the end of the log**, where failures are. Pass `start` to page forward with `nextStart`. Up to 1 MB per call |
| `jenkins_get_queue_info` | `/queue/api/json` | Queued builds in the base folder and why they're waiting |

All tools are read-only, so no CSRF crumb is needed. Crumb handling will be added with the first tool that changes something, such as triggering a build.

## Base folder and job paths

`JENKINS_BASE_FOLDER` (e.g. `Team` or `Team/sub-team`) scopes every tool to one folder. It is set **only** in the environment, never in code. Job paths in tool arguments and results are **relative to that folder**:

| `JENKINS_BASE_FOLDER` | Tool argument `job` | Jenkins URL |
|---|---|---|
| `Team` | `my-service/main` | `/job/Team/job/my-service/job/main/` |
| *(empty)* | `Team/my-service` | `/job/Team/job/my-service/` |

- Both `a/b` and the URL form `job/a/job/b` are accepted.
- Multibranch branches with `/` in their name use `%2F`, exactly as Jenkins shows them (`my-service/feature%2Flogin`).
- Every segment is URL-escaped, and empty, `.` and `..` segments are rejected, so a tool can't reach outside the base folder.
- The global build queue is filtered down to the base folder. `TeamX` does not match `Team`.

## Authentication

```text
Browser SSO ─▶ Jenkins sets cookie JSESSIONID.<suffix>
                          │ you copy the value once
                          ▼
           JENKINS_COOKIE_NAME + JENKINS_SESSION (or JENKINS_SESSION_FILE)
                          │
MCP tool ─▶ REST client ─▶ GET https://<jenkins>/job/<base>/…/api/json   Cookie: <name>=<value>
```

- The cookie **name** comes from `JENKINS_COOKIE_NAME` because its suffix differs per Jenkins instance. Nothing is hardcoded.
- **Expired sessions are detected.** When Jenkins treats the request as `anonymous`, or the SSO plugin redirects to the login page, the tool returns an `unauthorized` error telling you to refresh `JENKINS_SESSION`. Redirects are never followed.
- **Rotation:** with `JENKINS_SESSION_FILE`, the file is re-read on every request. Paste a fresh cookie value into it; there's no restart.
- **API tokens:** set `JENKINS_AUTH_MODE=api-token` with `JENKINS_USER` and `JENKINS_API_TOKEN`, using your own token from *Jenkins → your user → Security → API Token*.
- **Extensible:** authentication sits behind one small `Authenticator` interface, so another approved mechanism can be added without changing the client.

### Getting your session cookie

1. Sign in to Jenkins in the browser with SSO.
2. Open Chrome DevTools → **Application** → **Cookies** → your Jenkins host.
3. Copy the cookie **name** (e.g. `JSESSIONID.1a2b3c4d`) into `JENKINS_COOKIE_NAME`, and its **value** into `JENKINS_SESSION`.
4. Check it works:

   ```bash
   curl -s -H "Cookie: $JENKINS_COOKIE_NAME=$JENKINS_SESSION" "$JENKINS_URL/whoAmI/api/json"
   ```

   It should show your name, not `anonymous`.

## Credential safety

> [!IMPORTANT]
> Copying a browser session is meant for **local, single-user** use. Shared or production deployments must use an approved identity, for example a Jenkins API token of an approved service account with least-privilege permissions. They must not use a person's session.

- Secrets are read only from environment variables or files. They are never logged (secret-like log attributes are redacted) and never returned in tool results or errors.
- Error messages are built from Jenkins' diagnostic headers, never from HTML response bodies.
- `.env` is git-ignored. `.env.example` contains no secret values.
- This server never extracts cookies from a browser and never bypasses SSO. You copy your own session deliberately, and it expires on Jenkins' schedule.

## Quick start

Requirements: Go 1.27+ and [Task](https://taskfile.dev) (`go install github.com/go-task/task/v3/cmd/task@latest`).

```bash
cp .env.example .env    # fill in JENKINS_URL, JENKINS_BASE_FOLDER, JENKINS_COOKIE_NAME, JENKINS_SESSION
task build              # -> dist/jenkins-mcp
task smoke              # starts the server over stdio and lists the 7 tools
task test               # unit tests, no credentials needed
```

## Connecting a client (stdio)

### VS Code

Open this folder. `.vscode/mcp.json` defines the `jenkins` server. Start it from **MCP: List Servers**. VS Code prompts for the URL, base folder, cookie name and session, and stores the session as a secret.

### Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` on macOS, or `%APPDATA%\Claude\claude_desktop_config.json` on Windows:

```json
{
  "mcpServers": {
    "jenkins": {
      "command": "/absolute/path/to/dist/jenkins-mcp",
      "env": {
        "JENKINS_URL": "https://jenkins.example.com",
        "JENKINS_BASE_FOLDER": "Team",
        "JENKINS_COOKIE_NAME": "JSESSIONID.1a2b3c4d",
        "JENKINS_SESSION_FILE": "/Users/you/.config/jenkins-mcp/session"
      }
    }
  }
}
```

### Claude Code

```bash
claude mcp add jenkins \
  -e JENKINS_URL=https://jenkins.example.com -e JENKINS_BASE_FOLDER=Team \
  -e JENKINS_COOKIE_NAME=JSESSIONID.1a2b3c4d -e JENKINS_SESSION_FILE=$HOME/.config/jenkins-mcp/session \
  -- /absolute/path/to/dist/jenkins-mcp
```

### Docker (stdio)

```bash
task docker
docker run -i --rm --env-file .env jenkins-mcp:dev
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `JENKINS_URL` | — (required) | Jenkins URL or host. Must be https |
| `JENKINS_BASE_FOLDER` | *(root)* | Folder all job paths are relative to, e.g. `Team` or `Team/sub` |
| `JENKINS_AUTH_MODE` | `cookie` | `cookie` or `api-token` |
| `JENKINS_COOKIE_NAME` | — | Session cookie name, e.g. `JSESSIONID.1a2b3c4d` (cookie mode) |
| `JENKINS_SESSION` / `JENKINS_SESSION_FILE` | — | Your session cookie value, or a file holding it (cookie mode) |
| `JENKINS_USER` / `JENKINS_API_TOKEN` / `JENKINS_API_TOKEN_FILE` | — | Your user and API token (api-token mode) |
| `JENKINS_CA_FILE` | — | Extra CA bundle (PEM) |
| `JENKINS_INSECURE` | `false` | Skip TLS verification (lower environments only) |
| `JENKINS_REQUEST_TIMEOUT` | `30s` | Per-request timeout |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text`. Logs go to stderr, because stdout carries the MCP stream |

## Errors

Tool errors are MCP error results whose text is a JSON envelope:

```json
{"error":{"code":"unauthorized","message":"Jenkins did not accept the credential (request was treated as anonymous): the session is missing or expired. Sign in to Jenkins in the browser and refresh JENKINS_SESSION"}}
```

| Code | Meaning |
|---|---|
| `invalid_input` | The tool arguments failed validation. Jenkins was not called |
| `unauthorized` | The session or token is missing, invalid or expired |
| `forbidden` | Jenkins authorization denies the action. The message names the missing permission |
| `not_found` | The job or build doesn't exist, or you can't see it |
| `not_configured` | No credential is configured, or the secret file is empty |
| `timeout` | The request timed out |
| `upstream_error` | Network error or unexpected response |

## Architecture

```text
cmd/server/                composition root: config → auth → client → tools → transport
internal/config/           environment loading and validation, job-path parsing
internal/auth/jenkins/     Authenticator: session cookie or API token (static or file)
internal/clients/jenkins/  REST client: base-folder scoping, tree projections, console paging
internal/tools/jenkins/    MCP tool definitions, input validation, response shaping
internal/toolkit/          error envelope and validators
internal/mcp/              builds the MCP server from tool sets
internal/transport/        stdio (an HTTP transport can be added here)
internal/logging/          slog with secret redaction
```

The tools depend on a small interface rather than the HTTP client, so they're tested without a network. The client is tested against `httptest` mock Jenkins servers.

## Development

```bash
task test        # unit tests (mock servers, no credentials)
task race        # with the race detector
task vet
task lint        # golangci-lint
```
