# Stage 1: Build Clean-Room Web Console
FROM node:24-alpine AS console-builder
WORKDIR /src/console
COPY console/package*.json ./
RUN npm ci --no-audit --no-fund
COPY console/ ./
RUN npm run build

# Stage 2: Build Go single binaries (zero CGO dependencies)
FROM golang:1.23-alpine AS go-builder
WORKDIR /src
COPY server/ ./server/
COPY packages/cli-symbols/ ./packages/cli-symbols/
COPY demo-app/mock-api/ ./demo-app/mock-api/
RUN cd /src/server && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/caliper ./cmd/caliper \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/seed-replay ./cmd/seed-replay
RUN cd /src/packages/cli-symbols && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/caliper-symbols .
RUN cd /src/demo-app/mock-api && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/mock-api .

# Stage 3: Minimal production runtime (~25 MB)
FROM alpine:3.21
RUN apk add --no-cache ca-certificates curl
WORKDIR /app
COPY --from=go-builder /out/caliper /usr/local/bin/caliper
COPY --from=go-builder /out/seed-replay /usr/local/bin/seed-replay
COPY --from=go-builder /out/caliper-symbols /usr/local/bin/caliper-symbols
COPY --from=go-builder /out/mock-api /usr/local/bin/mock-api
COPY --from=console-builder /src/console/dist /app/console-dist
COPY demo-app/chaos-lab/symbols /app/demo-symbols
EXPOSE 8080 8090
ENTRYPOINT ["/usr/local/bin/caliper"]
CMD ["--addr", "0.0.0.0:8080", "--data-dir", "/data", "--console-dist", "/app/console-dist", "--alert-interval", "30s"]
