# build webui (Angular frontend)
FROM --platform=$BUILDPLATFORM node:20-alpine AS webui-builder
WORKDIR /webui
# Cache npm deps
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
# Build the Angular app
COPY webui/ ./
RUN npm run build

# build app
FROM --platform=$BUILDPLATFORM golang:1.23.6-alpine3.20 AS app-builder
RUN apk add --no-cache git tzdata

ENV SERVICE=bitmagnet

WORKDIR /src

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

COPY . ./

# Replace any committed webui/dist with the freshly built frontend
RUN rm -rf webui/dist
COPY --from=webui-builder /webui/dist ./webui/dist

ARG VERSION=dev
ARG REVISION=dev
ARG BUILDTIME
ARG TARGETOS TARGETARCH TARGETVARIANT

RUN --network=none \
export GOOS=$TARGETOS; \
export GOARCH=$TARGETARCH; \
[[ "$GOARCH" == "amd64" ]] && export GOAMD64=$TARGETVARIANT; \
[[ "$GOARCH" == "arm" ]] && [[ "$TARGETVARIANT" == "v6" ]] && export GOARM=6; \
[[ "$GOARCH" == "arm" ]] && [[ "$TARGETVARIANT" == "v7" ]] && export GOARM=7; \
echo $GOARCH $GOOS $GOARM$GOAMD64; \
go build -ldflags "-s -w \
  -X github.com/bitmagnet-io/bitmagnet/internal/version.GitTag=${VERSION} \
  -X github.com/bitmagnet-io/bitmagnet/internal/version.BuildTime=${BUILDTIME}" \
  -o /build/bitmagnet main.go

# build runner
FROM alpine:latest AS runner

LABEL org.opencontainers.image.source = "https://github.com/bitmagnet-io/bitmagnet"
LABEL org.opencontainers.image.licenses = "MIT"
LABEL org.opencontainers.image.base.name = "alpine:latest"

RUN apk --no-cache add ca-certificates curl tzdata jq iproute2-ss

COPY --link --from=app-builder /build/bitmagnet* /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/bitmagnet"]
