import { ApiError, consoleApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import ModelsCard from "@axiom/boards/AxApp/regions/ModelsCard";
import { Announcement, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

// General site information is read on demand, separately from mandatory account notices.
export function Announcements() {
  const { t } = useTranslation();
  const { t: common } = useTranslation("common");
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: [accountKey, "announcements"],
    enabled: open,
    queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/announcements", { signal })),
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
  });
  const status = query.error instanceof ApiError ? query.error.status : undefined;
  const items = query.isSuccess ? query.data.data : undefined;
  return (
    <ModelsCard t={String}>
      <details data-announcements onToggle={(event) => setOpen(event.currentTarget.open)}>
        <summary>{t("announcements.title")}</summary>
        {open && query.isPending && <p role="status">{t("announcements.loading")}</p>}
        {query.isError && (
          <div role="alert">
            <p>
              {status === 403
                ? common("state.forbiddenTitle")
                : t(status === 404 ? "announcements.unavailable" : "announcements.error")}
            </p>
            {status !== 403 && (
              <button type="button" onClick={() => query.refetch()}>
                {t("announcements.retry")}
              </button>
            )}
          </div>
        )}
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
