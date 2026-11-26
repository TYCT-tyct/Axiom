import { ApiError, consoleApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import ModelsCard from "@axiom/boards/AxApp/regions/ModelsCard";
import { Announcement, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

// seenKey holds when the newest announcement this browser has opened began. The server keeps no read state
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

// General site information, separate from mandatory account notices. The list is read with the page so the entry can
// say how many began after the newest one this browser opened.
export function Announcements() {
  const { t } = useTranslation();
  const [seen, setSeen] = useState(readSeen);
  const query = useQuery({
    queryKey: [accountKey, "announcements"],
    queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/announcements", { signal })),
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
    // A failed read waits for the entry's own retry rather than repeating on every page that draws the entry.
    retryOnMount: false,
  });
  const status = query.error instanceof ApiError ? query.error.status : undefined;
  const items = query.isSuccess ? query.data.data : undefined;
  const starts = (items ?? []).map((item) => instant(item.starts_at));
  const newest = Math.max(0, ...starts);
  const unread = starts.filter((at) => at > instant(seen)).length;
  function opened(open: boolean) {
    if (!open || newest === 0) return;
    const at = new Date(newest).toISOString();
    writeSeen(at);
    setSeen(at);
  }
  return (
    <ModelsCard t={String}>
      <details data-announcements onToggle={(event) => opened(event.currentTarget.open)}>
        <summary>
          {t("announcements.title")}
          {unread > 0 ? <span data-unread> {t("announcements.unread", { count: unread })}</span> : null}
        </summary>
        {query.isPending && <p role="status">{t("announcements.loading")}</p>}
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
