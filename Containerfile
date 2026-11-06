# Build:  podman build -t m3 .
#
# The final image is `scratch` plus ONE static binary and the CA bundle: no shell, no package
# manager, no interpreter, no OS packages. It runs as an unprivileged user.

# The build image is pinned by digest. Update it together with go.mod's Go version.
FROM docker.io/library/golang:1.26.10-alpine@sha256:2173d702003b1a8d17b844191acaeb36e94de46f6cabf2cb27370e151bbce5aa AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -trimpath -ldflags='-s -w' -o /out/m3 ./cmd/m3 \
 && go build -trimpath -ldflags='-s -w' -o /out/e2e-proxy ./tests/e2e/proxy

FROM scratch AS m3
# trust anchors for the ingest role (the web role makes no outbound connections)
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/m3 /m3
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/m3"]
CMD ["serve"]

# test helper (a minimal CONNECT proxy), only built on request: podman build --target e2e-proxy
FROM scratch AS e2e-proxy
COPY --from=build /out/e2e-proxy /e2e-proxy
USER 65532:65532
ENTRYPOINT ["/e2e-proxy"]
