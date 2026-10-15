# run.sh passes the pinned image refs (name:tag@sha256:...) from
# internal/stackversions/stackversions.yaml.
ARG GOLANG_IMAGE
ARG BASE_IMAGE

FROM ${GOLANG_IMAGE} AS build
ARG COLOR
WORKDIR /src
COPY . .
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=off GOFLAGS=-mod=mod
RUN go build -trimpath -ldflags "-s -w -X main.color=${COLOR}" -o /out/app . \
 && go build -trimpath -ldflags "-s -w" -o /out/load ./load

FROM ${BASE_IMAGE}
COPY --from=build /out/app /app
COPY --from=build /out/load /load
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
