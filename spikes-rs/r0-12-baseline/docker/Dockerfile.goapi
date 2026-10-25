# The Go comparison image: the Go golden app's API (CGO_ENABLED=0, -trimpath,
# -ldflags "-s -w", as the api pack Dockerfile.api) on distroless static.
#   docker build -f docker/Dockerfile.go --build-context bin=<dir with api> -t r0-12/api-go .
FROM scratch AS bin

FROM gcr.io/distroless/static-debian13:nonroot
ENV SPINUP_ENV=production
COPY --from=bin api /api
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/api"]
