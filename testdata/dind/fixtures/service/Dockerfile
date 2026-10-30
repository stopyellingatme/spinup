# The sidecar service image of the dind suite. run.sh replaces the [[ image ]] markers with the
# pinned refs from stackversions.yaml and copies this file to services/echo/Dockerfile.
# The binary has no dependency outside the standard library, so the build needs no module download.
FROM [[ image "golang" ]] AS build
WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS= go build -trimpath -ldflags "-s -w" -o /service main.go

FROM [[ image "distroless-static" ]]
COPY --from=build /service /service
USER nonroot
ENV PORT=8090
EXPOSE 8090
ENTRYPOINT ["/service"]
