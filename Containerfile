# The node-tracker container image. Build it with 'make image', which
# prepares the build context from the release archives in dist/ (see
# scripts/image.sh); this file does not work on the source tree.
FROM scratch

ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION
ARG REVISION
ARG CREATED
ARG SOURCE

LABEL org.opencontainers.image.title="node-tracker" \
	org.opencontainers.image.description="Alerts via Telegram when a Bitcoin node falls behind the chain tip" \
	org.opencontainers.image.version="${VERSION}" \
	org.opencontainers.image.revision="${REVISION}" \
	org.opencontainers.image.created="${CREATED}" \
	org.opencontainers.image.source="${SOURCE}" \
	org.opencontainers.image.url="${SOURCE}" \
	org.opencontainers.image.documentation="${SOURCE}/blob/main/docs/operations.md#container" \
	org.opencontainers.image.licenses="GPL-3.0-only"

# The modes come from the context; COPY --chmod would also set them on the
# parent directories with docker.
# /etc/ssl/certs/ca-certificates.crt
COPY rootfs/ /
# /state, mode 700, owned by the user below: a new volume there gets both.
COPY --chown=65532:65532 state-root/ /
# /node-tracker
COPY ${TARGETARCH}${TARGETVARIANT}/ /

USER 65532:65532
VOLUME /state
ENTRYPOINT ["/node-tracker", "run", "-config", "/config/config.yaml", "-state", "/state/state.json"]
