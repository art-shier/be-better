# syntax=docker/dockerfile:1.7

ARG NODE_IMAGE=node:24.7.0-alpine3.22@sha256:be4d5e92ac68483ec71440bf5934865b4b7fcb93588f17a24d411d15f0204e4f
ARG GO_IMAGE=golang:1.25.0-alpine3.22@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440
ARG RUNTIME_IMAGE=alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1
ARG CADDY_IMAGE=caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d
ARG POSTGRES_IMAGE=postgres:17.6-alpine3.22@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94

FROM ${NODE_IMAGE} AS web-build
WORKDIR /source
COPY package.json package-lock.json ./
COPY apps/web/package.json apps/web/package.json
RUN npm ci --workspace @dayorder/web
COPY apps/web apps/web
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
COPY deploy/Caddyfile /etc/caddy/Caddyfile
COPY --from=web-build --chown=caddy:caddy /source/apps/web/dist /srv
RUN chown -R caddy:caddy /data /config /srv
USER caddy
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
RUN apk add --no-cache pgbackrest && \
    mkdir -p /var/lib/pgbackrest /var/spool/pgbackrest /etc/pgbackrest && \
    chown -R postgres:postgres /var/lib/pgbackrest /var/spool/pgbackrest /etc/pgbackrest
COPY deploy/scripts/pgbackrest-wrapper.sh /usr/local/bin/dayorder-pgbackrest
RUN chmod 0555 /usr/local/bin/dayorder-pgbackrest
USER postgres
