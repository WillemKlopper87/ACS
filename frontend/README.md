# ACS operator console

React 19 + TypeScript + Vite front end for the ACS operator API.

```bash
npm ci
npm run dev        # http://localhost:5173, talks to VITE_API_BASE_URL (default http://localhost:8080)
npm run lint && npm run build && npm test -- --run
```

Screens are lazy-loaded chunks under `src/screens/`; the API client and
types live in `src/api/`. The console WebSocket and web-GUI iframe use
short-lived browser tickets (`POST /api/v1/auth/ticket`), never the
session JWT, in URLs. See the root [README](../README.md) for the
whole system.
