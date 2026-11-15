# Closes the "no container image" gap alongside the three backend
# services (see ACS/backend/Dockerfile.*).
#
# VITE_API_BASE_URL must be a build ARG, not a runtime env var: Vite
# inlines import.meta.env.* into the built JS at compile time (there's no
# server-side process to read a runtime env var from — this is a static
# SPA served by nginx below). A deployment pointing at a non-default API
# host must pass --build-arg VITE_API_BASE_URL=... at image build time.
# Base images are pinned by digest as well as by tag: a tag can be moved
# to different content, a digest cannot. Dependabot (docker) raises the
# digest when the tag is updated.
FROM node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS build
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

FROM nginx:1.31.5-alpine3.24@sha256:72ba65eb42c10344912a84ff42408db7d34f2feb642204570ab8fc5ffd29f1d3
# Take Alpine's security fixes as soon as they are published, without
# waiting for the next nginx tag (Trivy fails CI on fixable HIGH/CRITICAL).
RUN apk upgrade --no-cache
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
    # Run as the image's unprivileged "nginx" user, not root (security
    # assessment O14): no "user" directive, a pid file it can write, and
    # ownership of the few paths nginx writes to. Hence port 8080: an
    # unprivileged process cannot bind 80.
    sed -i '/^user /d' /etc/nginx/nginx.conf; \
    sed -i 's#^pid .*#pid /tmp/nginx.pid;#' /etc/nginx/nginx.conf; \
    chown -R nginx:nginx /var/cache/nginx /etc/nginx/conf.d; \
    nginx -t
USER nginx
EXPOSE 8080
