# Closes the "no container image" gap alongside the three backend
# services (see ACS/backend/Dockerfile.*).
#
# VITE_API_BASE_URL must be a build ARG, not a runtime env var: Vite
# inlines import.meta.env.* into the built JS at compile time (there's no
# server-side process to read a runtime env var from — this is a static
# SPA served by nginx below). A deployment pointing at a non-default API
# host must pass --build-arg VITE_API_BASE_URL=... at image build time.
FROM node:22-alpine AS build
WORKDIR /src
# .npmrc carries legacy-peer-deps (TypeScript 6 vs openapi-typescript's ^5 peer)
COPY package.json package-lock.json .npmrc ./
RUN npm ci
COPY . .
ARG VITE_API_BASE_URL=http://localhost:8080
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL
# Optional environment badge next to the brand ("staging", "production",
# …). Same build-time-inlining constraint as the API base URL above.
# Unset by default and then not rendered at all — the badge used to be
# hardcoded to "dev" and shipped that way everywhere, so no badge is the
# safer default than a wrong one.
ARG VITE_ENV_LABEL=
ENV VITE_ENV_LABEL=$VITE_ENV_LABEL
RUN npm run build

FROM nginx:1.31.5-alpine3.24
# Re-declared: an ARG from an earlier stage is not in scope here. This is
# deliberately the *same* argument the bundle was built with above,
# because the CSP below must name exactly the origin that JS will call.
# When the two were maintained separately — a hardcoded localhost:8080 in
# nginx.conf, a build arg for the bundle — every non-localhost deploy
# silently had all its API calls blocked by the browser, since a CSP
# violation doesn't surface as a request error in the app. One knob now
# feeds both.
ARG VITE_API_BASE_URL=http://localhost:8080
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf.template /tmp/default.conf.template
# Renders the template, then `nginx -t` validates the result — so a bad
# VITE_API_BASE_URL fails the build rather than the first request. The
# case statement rejects a scheme-less value for the same reason: it
# would otherwise produce a CSP that silently matches nothing.
RUN set -eu; \
    api="${VITE_API_BASE_URL%/}"; \
    case "$api" in \
      https://*) ws="wss://${api#https://}" ;; \
      http://*)  ws="ws://${api#http://}"   ;; \
      *) echo "VITE_API_BASE_URL must be an absolute http(s) URL, got: '$api'" >&2; exit 1 ;; \
    esac; \
    export API_ORIGIN="$api" API_WS_ORIGIN="$ws"; \
    envsubst '$API_ORIGIN $API_WS_ORIGIN' \
      < /tmp/default.conf.template > /etc/nginx/conf.d/default.conf; \
    rm /tmp/default.conf.template; \
    nginx -t
EXPOSE 80
