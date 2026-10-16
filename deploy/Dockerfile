# The program is built on the host by `just up`, so the image needs no toolchain image and scratch adds nothing to
# trust or patch beyond the program itself. It runs as an unprivileged user and checks its own health, as there is no
# shell in the image. It has no certificate store either: the program embeds the root certificates it needs for outbound
# HTTPS (cmd/axiom/main.go).
FROM scratch
COPY bin/axiom /axiom
USER 65532:65532
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --start-interval=1s --retries=3 CMD ["/axiom", "healthcheck"]
ENTRYPOINT ["/axiom"]
CMD ["serve"]
