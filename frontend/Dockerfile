# Closes the "no container image" gap alongside the three backend
# services (see ACS/backend/Dockerfile.*).
#
# VITE_API_BASE_URL must be a build ARG, not a runtime env var: Vite
# inlines import.meta.env.* into the built JS at compile time (there's no
# server-side process to read a runtime env var from — this is a static
# SPA served by nginx below). A deployment pointing at a non-default API
# host must pass --build-arg VITE_API_BASE_URL=... at image build time.
FROM node:26-alpine AS build
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
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
