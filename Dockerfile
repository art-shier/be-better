# syntax=docker/dockerfile:1.7

ARG NODE_IMAGE=node:24.15.0-alpine3.22@sha256:b689d4005875ae167178471a7a622ec2909459a3bbb32277260be1971af7a99f
ARG GO_IMAGE=golang:1.26.8-alpine3.23@sha256:a8fa79c5bd40d880b52bd3b6d7669ecdcfd00e85facdd427d279efb5ddd79cb1
ARG RUNTIME_IMAGE=alpine:3.22.6@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
ARG CADDY_IMAGE=caddy:2.11.4-alpine@sha256:6aeddd44c3078b0f9a35206472a11420648a79c184603ef95957d0a20044cb2b
ARG POSTGRES_IMAGE=postgres:17.10-alpine3.22@sha256:b02d9b5bcf608c2719da32cdabee274a33841202487fd5dc9b065b63f886753f

FROM ${NODE_IMAGE} AS web-build
WORKDIR /source
COPY package.json package-lock.json ./
COPY apps/web/package.json apps/web/package.json
RUN npm ci --workspace @dayorder/web
COPY apps/web apps/web
COPY contracts/agent contracts/agent
RUN npm run build:web

FROM ${GO_IMAGE} AS go-build
WORKDIR /source
COPY go.work go.work.sum ./
COPY apps/api/go.mod apps/api/go.sum apps/api/
RUN --mount=type=cache,target=/go/pkg/mod go -C apps/api mod download
COPY apps/api apps/api
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go -C apps/api build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/dayorder-api ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go -C apps/api build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/dayorder-worker ./cmd/worker && \
    CGO_ENABLED=0 GOOS=linux go -C apps/api build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/dayorder-migrate ./cmd/migrate

FROM ${GO_IMAGE} AS caddy-build
WORKDIR /source
ENV GOWORK=off
COPY deploy/caddy/go.mod deploy/caddy/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY deploy/caddy/main.go ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -mod=readonly -buildvcs=false -trimpath -ldflags="-s -w" -o /out/caddy .

FROM ${GO_IMAGE} AS gosu-build
WORKDIR /source
ENV GOWORK=off
COPY deploy/gosu/go.mod deploy/gosu/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -mod=readonly -buildvcs=false -trimpath -ldflags="-s -w" -o /out/gosu github.com/tianon/gosu

FROM ${RUNTIME_IMAGE} AS runtime-base
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 dayorder && \
    adduser -S -D -H -u 10001 -G dayorder dayorder
COPY deploy/scripts/container-entrypoint.sh /usr/local/bin/dayorder-entrypoint
RUN chmod 0555 /usr/local/bin/dayorder-entrypoint
USER dayorder
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/dayorder-entrypoint"]

FROM runtime-base AS api
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="DayOrder API" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
COPY --from=go-build --chown=10001:10001 /out/dayorder-api /app/dayorder-api
EXPOSE 8080 9090
CMD ["/app/dayorder-api"]

FROM runtime-base AS worker
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="DayOrder Worker" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
COPY --from=go-build --chown=10001:10001 /out/dayorder-worker /app/dayorder-worker
EXPOSE 9091
CMD ["/app/dayorder-worker"]

FROM runtime-base AS migrate
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="DayOrder Migrator" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
COPY --from=go-build --chown=10001:10001 /out/dayorder-migrate /app/dayorder-migrate
CMD ["/app/dayorder-migrate"]

FROM ${CADDY_IMAGE} AS web
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="DayOrder Web" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
USER root
RUN setcap -r /usr/bin/caddy
COPY --from=caddy-build /out/caddy /usr/bin/caddy
COPY deploy/Caddyfile /etc/caddy/Caddyfile
COPY --from=web-build --chown=10001:10001 /source/apps/web/dist /srv
RUN chown -R 10001:10001 /data /config /srv
USER 10001:10001
EXPOSE 8080 8081 8443 2019

FROM ${POSTGRES_IMAGE} AS postgres
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="DayOrder PostgreSQL with pgBackRest" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
USER root
COPY --from=gosu-build /out/gosu /usr/local/bin/gosu
RUN apk upgrade --no-cache && \
    apk add --no-cache pgbackrest && \
    mkdir -p /var/lib/pgbackrest /var/spool/pgbackrest /etc/pgbackrest && \
    chown -R postgres:postgres /var/lib/pgbackrest /var/spool/pgbackrest /etc/pgbackrest
COPY deploy/scripts/pgbackrest-wrapper.sh /usr/local/bin/dayorder-pgbackrest
RUN chmod 0555 /usr/local/bin/dayorder-pgbackrest
USER postgres
