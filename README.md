# OpenVaultDB To-Do Demo

A realistic third-party to-do app that uses **OpenVaultDB (OVDB)** as its
database. It demonstrates the OVDB *connect flow*: the app asks the user to
connect their vault, receives a scoped token, and stores every task as a record
in the user's vault — the app never owns the data.

```
┌──────────────┐  REST (CORS)  ┌──────────────┐  scoped token   ┌──────────────┐
│  Frontend    │ ───────────►  │  Go backend  │ ─────────────►  │  OVDB server │
│  Vite + TS   │               │  cobra+fang  │   record CRUD   │   :8088      │
│  :5173       │ ◄───────────  │  :5180       │ ◄─────────────  │              │
└──────────────┘   tasks JSON  └──────────────┘                 └──────────────┘
```

The **frontend never calls the OVDB server directly.** All task reads/writes go
frontend → demo backend → OVDB. The backend holds the connect credentials and
the scoped vault token (in-memory) server-side.

<!-- dev-approach:v1 -->
## Our approach to development

We build with our own tooling:

- **[SpecScore](https://specscore.md)** — specify requirements as `SpecScore.md` artifacts
- **[SpecStudio](https://specscore.studio)** — author & manage specs across their lifecycle
- **[inGitDB](https://ingitdb.com)** — store structured data in Git where applicable
- **[DALgo](https://dalgo.io)** — data access layer for Go
- **[cover100.dev](https://cover100.dev)** — drive toward 100% test coverage
- **[DataTug](https://datatug.io)** — query & explore data
<!-- /dev-approach -->

## Structure

```
backend/                      Go REST API + OVDB connect-flow proxy (:5180)
  cmd/todo-backend/main.go    thin entry point → internal/cli.Run
  internal/cli/cli.go         cobra root + `serve --port` (charm.land/fang/v2)
  internal/server/server.go   HTTP routes, sessions, REST API, CORS
  internal/ovdb/ovdb.go       thin OVDB HTTP client (authorize/token + record CRUD)
frontend/                     Vite + TypeScript UI (:5173)
  src/api.ts                  typed client for the demo BACKEND only
  src/main.ts                 connect button + add/list/toggle/delete UI
  public/.well-known/openvaultdb.yaml   app manifest (client_id, namespace, roles)
```

## Prerequisites

- Go 1.26+
- Node 18+ / npm

## Run everything together

Start the three pieces in this order, each in its own terminal.

### 1. OVDB server on :8088 (built separately)

```sh
ovdb-server serve --port 8088
```

On startup it prints `OWNER_TOKEN=<token>` and seeds the `local` vault, the
`todo-demo.openvaultdb.app/openvaultdb/todos` namespace, and the `tasks`
collection. The to-do demo does **not** need the owner token (that is for the
wallet); it obtains its own scoped token through the connect flow.

### 2. Demo backend on :5180

```sh
cd backend
go build ./...
go run ./cmd/todo-backend serve --port 5180
```

### 3. Frontend on :5173

```sh
cd frontend
npm install
npm run dev
```

Open <http://localhost:5173>, click **Connect your vault**, approve on the OVDB
consent screen, and you are returned to the app with a connected vault. Add,
complete, and delete tasks — each is a record in your vault.

## The connect flow

1. The user clicks **Connect** → browser navigates to `GET :5180/connect`.
2. The backend 302-redirects to the OVDB authorize endpoint with the pinned
   params:
   ```
   GET http://localhost:8088/authorize
       ?client_id=todo-demo.openvaultdb.app
       &redirect_uri=http://localhost:5180/callback
       &vault=local
       &namespaceId=todo-demo.openvaultdb.app/openvaultdb/todos
       &role=editor
       &state=<random>
   ```
3. OVDB shows a consent screen, then 302s to
   `:5180/callback?code=…&state=…`.
4. The backend verifies `state`, then `POST :8088/token` with the code and
   stores the returned scoped `access_token` server-side. It 302s back to the
   frontend (`:5173`).
5. Task ops: frontend → `:5180/api/tasks` → backend → OVDB record endpoints
   (`/vaults/local/ns/<ns-url-encoded>/collections/tasks/records[/{id}]`) with
   the stored bearer token. The namespace id's `/` are URL-encoded as `%2F`.

## Backend REST API (consumed by the frontend)

| Method | Path | Body | Purpose |
|--------|------|------|---------|
| GET | `/api/status` | – | `{ "connected": bool }` |
| GET | `/api/tasks` | – | list tasks |
| POST | `/api/tasks` | `{ title }` | create a task |
| PATCH | `/api/tasks/{id}` | `{ done?, title? }` | update a task |
| DELETE | `/api/tasks/{id}` | – | delete a task |
| GET | `/connect` | – | 302 → OVDB authorize |
| GET | `/callback` | – | token exchange, 302 → frontend |

CORS allows origin `http://localhost:5173`.

A task maps 1:1 to a `tasks` record `{ id, title, done, createdAt }`. The
backend sets `title`/`done` on create; `id` and `createdAt` are server-set by
OVDB.

## Assumptions / stubs

- **Single in-memory session.** The backend stores one scoped token process-wide
  (no per-user sessions, no persistence). Restarting the backend requires
  re-connecting. A real app would key sessions to authenticated users.
- **No token refresh.** The scoped token is used until the process exits; expiry
  is not handled.
- **No live OVDB during build.** This app was built strictly to the
  `interface/main.tsp` contract and the `INTEGRATION.md` constants; it does not
  ship a mock. Endpoints that need a token return `401 not connected` until the
  connect flow completes against a running OVDB server.
