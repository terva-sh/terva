# Container image for terva — ghcr.io/terva-sh/terva.
#
# Built by goreleaser during the public release (the `dockers_v2`
# block in .goreleaser.yaml): the build context is a temp dir holding
# the prebuilt linux binaries per platform ($TARGETPLATFORM/terva), so
# this is NOT a standalone `docker build .` — to build the image
# locally, run `goreleaser release --snapshot --clean --skip=publish`
# and pick the snapshot tag it prints.
#
# alpine over distroless on purpose: terva is a coding agent, and its
# bash tool wants a real shell with the usual companions (git, curl) —
# but deliberately NO interpreters. Script extensions and npx/uvx MCP
# servers belong in a derived image (examples/deploy/docker/
# Dockerfile.tools); the extension/MCP container story is documented
# in docs/deploy.md.
FROM alpine:3.22

RUN apk add --no-cache bash git curl ca-certificates openssh-client tzdata \
	&& addgroup -S terva && adduser -S -G terva -h /home/terva terva \
	&& mkdir -p /data /work && chown terva:terva /data /work

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/terva /usr/local/bin/terva

USER terva

# TERVA_HOME on a volume: config, credentials (bot tokens, 0600),
# sessions, extensions, and logs all live here — mount it to persist
# across container replacements.
ENV TERVA_HOME=/data
# /work is the agent's workspace (what its tools see); mount a project
# or a scratch directory.
WORKDIR /work
VOLUME ["/data", "/work"]

ENTRYPOINT ["terva"]
CMD ["--help"]
