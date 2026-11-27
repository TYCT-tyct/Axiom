import { ApiError, consoleApi, unwrap } from "@axiom/api";
import { accountKey, useSession } from "@axiom/app";
import ModelsCard from "@axiom/boards/AxApp/regions/ModelsCard";
import { Announcement, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

// seenKey holds when the newest announcement began the last time this browser opened the list. The server keeps no read state
// (docs/domains/accounts.md「用户提醒与站内公告」), so the unread mark is this browser's own convenience: without storage
// every announcement reads as new, which is the safe side.
const seenKey = "axiom.announcements.seen";

function readSeen() {
  try {
    return localStorage.getItem(seenKey) ?? "";
  } catch {
    return "";
  }
}

function writeSeen(at: string) {
  try {
    localStorage.setItem(seenKey, at);
  } catch {
    // Storage refused: the mark comes back on the next visit, nothing else depends on it.
  }
}

// ReadFailed says why the list could not be read: a refusal of the permission, a node without announcements, or a
// failure worth another try.
function ReadFailed({ status, retry }: { status: number | undefined; retry: () => void }) {
  const { t } = useTranslation();
  const { t: common } = useTranslation("common");
  return (
    <div role="alert">
      <p>
        {status === 403
          ? common("state.forbiddenTitle")
          : t(status === 404 ? "announcements.unavailable" : "announcements.error")}
      </p>
      {status !== 403 && (
        <button type="button" onClick={retry}>
          {t("announcements.retry")}
        </button>
      )}
    </div>
  );
}

// instant is a timestamp as milliseconds, 0 when absent: they are compared as instants, not as text, since the API may
// write the same instant with another offset.
function instant(at: string | undefined) {
  return Date.parse(at ?? "") || 0;
}

// General site information is read on demand, separately from mandatory account notices. The session says when the
// newest live one began, so the entry marks one this browser has not opened without reading the list.
export function Announcements() {
  const { t } = useTranslation();
  const latest = useSession().latest_announcement_at;
  const [seen, setSeen] = useState(readSeen);
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: [accountKey, "announcements"],
    enabled: open,
    queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/announcements", { signal })),
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
  });
  const status = query.error instanceof ApiError ? query.error.status : undefined;
  const items = query.isSuccess ? query.data.data : undefined;
  const unread = instant(latest) > instant(seen);
  function toggled(now: boolean) {
    setOpen(now);
    if (!now || latest === undefined) return;
    writeSeen(latest);
    setSeen(latest);
  }
  return (
    <ModelsCard t={String}>
      <details data-announcements onToggle={(event) => toggled(event.currentTarget.open)}>
        <summary>
          {t("announcements.title")}
          {unread ? <span data-unread> {t("announcements.unread")}</span> : null}
        </summary>
        {open && query.isPending && <p role="status">{t("announcements.loading")}</p>}
        {query.isError && <ReadFailed status={status} retry={() => query.refetch()} />}
        {query.isSuccess && items === undefined && <p>{t("announcements.unavailable")}</p>}
        {items?.length === 0 && <p>{t("announcements.empty")}</p>}
        {items?.map((item, index) => (
          <Announcement key={item.id ?? index} title={item.title ?? t("announcements.unavailable")}>
            <Text tone="muted">{item.body ?? t("announcements.unavailable")}</Text>
          </Announcement>
        ))}
      </details>
    </ModelsCard>
  );
}
