# The backup job's own image (docs/ops/backup-restore.md). It is not part of the program's image: pg_dump must be at
# least as new as the server it dumps, so it comes from the postgres image the database already runs, and the encryption
# and upload tools plus the bucket credentials must never sit inside the image facing the internet.
FROM postgres:18.6-alpine

# ca-certificates is not in the postgres image, and rclone needs it to verify the bucket's TLS chain; bash is what the
# scripts are written in.
RUN apk add --no-cache age bash ca-certificates rclone

COPY common.sh backup.sh restore.sh /ops/
RUN chmod 0755 /ops/*.sh

# The backup is what a bare `docker run` of this image should do; deploy/compose.yaml picks the job per service. Root,
# because writing the key ring back has to set its owner.
ENTRYPOINT ["/ops/backup.sh"]
