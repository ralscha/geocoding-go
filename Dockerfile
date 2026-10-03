# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go run ./internal/releaselicenses -out /out/licenses/NOTICE.txt
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /out/geocode ./cmd/geocode

FROM scratch
COPY --from=build /out/geocode /geocode
COPY LICENSE /licenses/
COPY --from=build /out/licenses/ /licenses/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/geocode"]
CMD ["serve", "-db", "/data/geocode.db", "-addr", ":8080"]
