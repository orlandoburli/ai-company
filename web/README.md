# AI Company — web interface

The Next.js App Router client for the AI Company control plane. It talks to the
Go API in the repository root; `/api/*` is proxied to `http://localhost:8080`
(see `next.config.ts`), so the HttpOnly session cookie stays same-origin and is
never read from JavaScript.

## Run

The backend, migrations and seed data are driven from the repository root:

```sh
make dev          # migrations + seed + API :8080 + this app :3000
```

Or run this side alone against an API that is already up:

```sh
npm install
npm run dev
```

Sign in with the seeded owner (`owner@acme.dev` /
`correct-horse-battery-staple`) or create a company from `/register`.

## Checks

```sh
npx tsc --noEmit
npx eslint src
npm run build
```

## Layout

- `src/app/(app)/` — authenticated shell: board, backlog, queue, item detail,
  settings. The group's layout resolves `/api/auth/me` before rendering.
- `src/app/login`, `register`, `accept-invite` — public auth flows.
- `src/lib/api.ts` — the only place that calls `fetch`; typed against the Go
  JSON contracts in `types.ts`.
- `src/components/` — app shell, item bits, dialogs and primitives.

The queue is ordered by the backend (`pinned` before base score); the client
only renders it. Status changes on the board go through
`PATCH /api/items/{id}` or `POST /api/items/bulk-status`, never a client-side
reordering.
