# syntax=docker/dockerfile:1
#
# Cadence — one static binary that serves the API and the web app from the same
# origin. Three stages: build the SPA, embed it into the Go binary, ship it on a
# distroless base (no shell, non-root, ~15 MB).
#
#   docker build -t cadence .
#   docker run --rm -p 8088:8088 -e CADENCE_BACKEND=memory cadence
#
# In practice `docker compose up -d` builds this and wires it to Postgres.

# ---- web: Vite build into the path go:embed reads ---------------------------
FROM node:22-alpine AS web
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY index.html vite.config.ts tsconfig.json tsconfig.app.json tsconfig.node.json ./
COPY src ./src
# VITE_API_URL="" makes the app call its own origin — the server that serves it.
RUN VITE_API_URL="" npm run build -- --outDir server/internal/web/dist --emptyOutDir

# ---- api: static Go binary with the SPA embedded ----------------------------
FROM golang:1.25-alpine AS api
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server ./
COPY --from=web /src/server/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/cadence-server ./cmd/cadence-server

# ---- runtime ----------------------------------------------------------------
# distroless/static: CA roots + tzdata + a nonroot user, nothing else. The
# HEALTHCHECK uses the binary's own `health` subcommand because there is no curl.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=api /out/cadence-server /cadence-server
ENV CADENCE_ADDR=:8088
EXPOSE 8088
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/cadence-server", "health"]
ENTRYPOINT ["/cadence-server"]
