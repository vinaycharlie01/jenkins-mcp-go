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
| `jenkins_get_build_images` | build `api/json` + console tail + artifacts | **Images the build pushed**: repository, tag, **registry digest**, evidence line, and an `expected` list ready for `argocd_get_images` |

All tools are read-only, so no CSRF crumb is needed. Crumb handling will be added with the first tool that changes something, such as triggering a build.

## Debugging failed builds

| Priority | Tool | Jenkins API | What it answers |
|---|---|---|---|
| P0 | `jenkins_get_failed_stages` | `…/{build}/wfapi/describe` | Which Pipeline stage failed, with its error, plus every stage's status and duration |
| P0 | `jenkins_get_stage_details` | `…/execution/node/{id}/wfapi/describe` | The steps of one stage (by name or id): command, status, duration, error, agent |
| P0 | `jenkins_get_failed_step_logs` | `…/wfapi/describe?fullStages=true` + `…/execution/node/{id}/wfapi/log` | **The actual error output**: the end of each failed step's log, with its command |
| P0 | `jenkins_get_test_results` | `…/{build}/testReport/api/json` (small projection) | Totals, new failures (regressions), fixed tests, and the suites with the most failures |
| P0 | `jenkins_get_test_failures` | `…/{build}/testReport/api/json` | Failed tests with error, trimmed stack trace and age. New failures come first. Filter with `search` |
| P1 | `jenkins_compare_builds` | build `api/json` ×2 + `wfapi` + `testReport` | Good vs bad build (default `lastSuccessfulBuild` vs `lastBuild`): result, duration, **parameter changes**, Git commit, agent, per-stage status, tests, and the **commits in between** |
| P1 | `jenkins_get_build_changes` | build `api/json` (`changeSets`, `culprits`) | Commits a build picked up: author, time, message, changed files; plus culprits and the Git revision |

Notes:
- The `build` input defaults to `lastBuild`; use `lastFailedBuild` for the latest failure. A permalink is resolved to a number once, so every call reads the same build.
- **Requirements:**
  - The stage tools need the **Pipeline Stage View** plugin (`wfapi`), which is installed with the suggested plugins. On a freestyle job, or without the plugin, they return `not_found` with that explanation.
  - The test tools need the job to publish **JUnit** results (`junit '**/target/*.xml'`). Without them they return `not_found`.
- **Step logs:** Jenkins' `wfapi/log` returns about **the last 10 KB** of a step's output, as HTML (the limit can be changed with the `com.cloudbees.workflow.rest.external.FlowNodeLogExt.maxReturnChars` system property). The tool converts it to plain text and sets `logTruncated`. Use `jenkins_get_console_logs` for the full log.
- **Suggested flow for "why did it fail?":** `jenkins_get_failed_stages` → `jenkins_get_failed_step_logs` → `jenkins_get_test_failures` (if tests failed) → `jenkins_compare_builds` (what changed since the last green build).

## Images a build produced (`jenkins_get_build_images`)

The tool reads the **end of the console log** (default 4 MiB, `scanBytes` up to 32 MiB) and small image or digest artifacts, and recognizes:

| Tool | Log line | Gives |
|---|---|---|
| `docker push` | `The push refers to repository [r/app]` … `1.4.2: digest: sha256:… size: …` | tag + digest |
| `docker buildx` / BuildKit | `pushing manifest for r/app:1.4.2@sha256:…` | tag + digest |
| Kaniko | `Pushing image to r/app:1.4.2` … `Pushed r/app@sha256:…` | tag + digest |
| Jib | `Built and pushed image as r/app:1.4.2` (+ a `jib-image.digest` artifact if archived) | tag (+ digest) |
| `docker build` (not pushed) | `Successfully tagged …` / `naming to …` | tag, marked `pushed: false` |
| Your pipeline | `JENKINS_IMAGE_PATTERN` (see below) | whatever your pattern captures |

- Base images (`FROM node@sha256:…`) and `docker pull` digests are **not** reported.
- Artifacts whose path looks like `*digest*.txt`, `*image*.json` or `*.digest` are read, up to 5 files of 64 KiB each.
- Build parameters named like `IMAGE`, `TAG`, `VERSION`, `DIGEST` or `SHA`, plus the display name and the Git commit, are returned as **hints**.

### Comparing with Argo CD

The output includes `expected`, which is exactly the input [`argocd_get_images`](https://github.com/vinaycharlie01/argocd-mcp-go) takes. With both MCP servers connected, ask:

> *"Is the image from Jenkins build 42 of payments/main running in Argo CD app payments?"*

Argo CD then answers `MATCH`, `MIXED_ROLLOUT`, `TAG_MATCH_DIGEST_DIFFERENT`, `DIFFERENT_TAG`, and so on, by comparing **digests** rather than only tags.

### Custom pattern (`JENKINS_IMAGE_PATTERN`)

If your pipeline prints images differently, add one line to `.env`. The pattern is a Go regular expression with named groups `image`, plus optional `tag` and `digest`:

```bash
# Pipeline prints: PUBLISHED registry.example.com/team/svc:1.2.3 sha256:abc...
JENKINS_IMAGE_PATTERN=PUBLISHED (?P<image>\S+) (?P<digest>sha256:[0-9a-f]{64})
```

The pattern is validated at startup, so an invalid one stops the server with a clear error.

> [!TIP]
> To get digests reliably, make the pipeline print them, for example `docker push` (which already prints them), `docker buildx build --push`, `crane digest <image>`, or archive an `image-digest.txt`.

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
| `JENKINS_IMAGE_PATTERN` | — | Optional regex with named groups `image`, `tag`, `digest` for custom image output |
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
internal/imageref/         image reference parsing/normalization (tag, registry digest)
internal/imagescan/        finds pushed images in console logs and artifacts
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
