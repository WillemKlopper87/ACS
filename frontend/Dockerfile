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
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
ARG VITE_API_BASE_URL=http://localhost:8080
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL
RUN npm run build

FROM nginx:1.31-alpine
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
