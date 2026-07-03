# Build context is provided by goreleaser: the prebuilt static binary is the
# only input. distroless/static ships CA certs + tzdata, no shell, runs nonroot
# — the right posture for a tool whose job is executing untrusted web content.
FROM gcr.io/distroless/static-debian12:nonroot
COPY unblink /usr/local/bin/unblink
ENTRYPOINT ["/usr/local/bin/unblink"]
