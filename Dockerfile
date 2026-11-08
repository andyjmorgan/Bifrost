# mcp-gateway: Go service (MCP front door + REST API + worker) serving the React SPA.
FROM node:22-alpine AS frontend
WORKDIR /web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
COPY --from=frontend /web/dist/ /app/web/
ENV MCPGW_LISTEN_ADDR=:8080 MCPGW_WEBROOT=/app/web MCPGW_VERSION=$VERSION
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/gateway"]
