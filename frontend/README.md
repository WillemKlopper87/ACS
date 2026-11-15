# ACS operator console

React 19 + TypeScript + Vite front end for the ACS operator API.

```bash
npm ci
npm run dev        # http://localhost:5173, talks to VITE_API_BASE_URL (default http://localhost:8080)
npm run lint && npm run build && npm test -- --run
```

Screens are lazy-loaded chunks under `src/screens/`; the API client and
types live in `src/api/` (`generated.ts` comes from the API description:
`npm run generate:api` after changing `backend/openapi.yaml`).

**Session.** Signing in sets an `HttpOnly`, `SameSite=Strict` cookie
(`acs_session`); the console never sees or stores the token. Requests go
out with `credentials: "include"` and, when they change something, an
`X-ACS-CSRF` header (`src/api/client.ts`, `src/auth/`). The console and
the API therefore have to be the same site: `localhost:5173` with
`localhost:8080` in development, one HTTPS origin or two names under one
domain when deployed. Against an older API that sets no cookie the
console falls back to keeping the token in memory for the tab.

The console WebSocket and web-GUI iframe use short-lived browser tickets
(`POST /api/v1/auth/ticket`), each tied to one device and one purpose,
never the session in a URL.

```bash
npm run e2e        # Playwright, against a mock API (16 tests, runs in CI)
```

The production image (`Dockerfile`) is unprivileged nginx listening on
8080, not 80. Current screens: `docs/screenshots/`. See the root
[README](../README.md) for the whole system.
