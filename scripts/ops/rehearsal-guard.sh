#!/bin/bash
# The rehearsal's guard around everything that writes into a bucket (scripts/ops/rehearse.sh): the backup job, which
# deploy/compose.rehearsal.yaml starts through it, and the rehearsal's own writes. It checks the values compose resolved
# for this container, not what the script meant to write, and runs before the command, because an upload and the pruning
# after it cannot be undone. The audit replica's buckets are the rehearsal's too, under the same endpoint and key.
set -euo pipefail
for want in AXIOM_BACKUP_BUCKET=axiom-rehearsal AXIOM_BACKUP_PREFIX=axiom AXIOM_OBJECTSTORE_ENDPOINT=http://s3:9000 \
  AXIOM_OBJECTSTORE_ACCESS_KEY_ID=rehearsal; do
  name=${want%%=*} value=${want#*=} got=${!name-}
  if [ "$got" != "$value" ]; then
    printf 'rehearsal: %s is %s, not %s: refusing to write\n' "$name" "${got:-<empty>}" "$value" >&2
    exit 1
  fi
done
case ${AXIOM_OBJECTSTORE_BUCKET-} in
axiom-rehearsal | axiom-rehearsal-audit | axiom-rehearsal-audit-2) ;;
*)
  printf 'rehearsal: AXIOM_OBJECTSTORE_BUCKET is %s, not one of the rehearsal buckets: refusing to write\n' \
    "${AXIOM_OBJECTSTORE_BUCKET:-<empty>}" >&2
  exit 1
  ;;
esac
exec "$@"
