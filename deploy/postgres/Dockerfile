# The database image: the pinned PostgreSQL tag plus pgBackRest, the WAL archive tool
# (docs/ops/backup-restore.md「WAL 连续归档与时间点恢复」). The program image stays separate: the archive needs a shell and
# the bucket credentials, neither of which belongs in the image facing the internet.
#
# pgBackRest comes from the Alpine release the base image already uses, pinned to the version the runbook was checked
# against. 2.58.0 is the first release that accepts an http:// object-store endpoint, which the SeaweedFS test needs, and
# it supports PostgreSQL 18.
FROM postgres:18.6-alpine

RUN apk add --no-cache pgbackrest=2.58.0-r0 \
    && install -d -o postgres -g postgres /var/lib/pgbackrest

COPY pgbackrest.conf /etc/pgbackrest/pgbackrest.conf
COPY entrypoint.sh pitr.sh basebackup.sh /usr/local/bin/
RUN chmod 0755 /usr/local/bin/entrypoint.sh /usr/local/bin/pitr.sh /usr/local/bin/basebackup.sh \
    && chown root:postgres /etc/pgbackrest/pgbackrest.conf \
    && chmod 0640 /etc/pgbackrest/pgbackrest.conf

# The base image's entrypoint is still the one that initializes the cluster; this wrapper only appends the archive
# settings when the bucket is configured, so a deployment without backups runs exactly as before.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["postgres"]
