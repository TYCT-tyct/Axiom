import DesktopUsage from "@axiom/boards/AxApp/regions/Usage";
import desktopEn from "@axiom/boards/AxApp/regions/Usage.en";
import desktopZh from "@axiom/boards/AxApp/regions/Usage.zh";
import DesktopCalendar from "@axiom/boards/AxApp/regions/UsageCalendar";
import desktopCalEn from "@axiom/boards/AxApp/regions/UsageCalendar.en";
import desktopCalZh from "@axiom/boards/AxApp/regions/UsageCalendar.zh";
import PhoneUsage from "@axiom/boards/AxAppPhone/regions/Usage";
import phoneEn from "@axiom/boards/AxAppPhone/regions/Usage.en";
import phoneZh from "@axiom/boards/AxAppPhone/regions/Usage.zh";
import PhoneCalendar from "@axiom/boards/AxAppPhone/regions/UsageCalendar";
import phoneCalEn from "@axiom/boards/AxAppPhone/regions/UsageCalendar.en";
import phoneCalZh from "@axiom/boards/AxAppPhone/regions/UsageCalendar.zh";
import { Button, DataState, EmptyState, usePageTitle } from "@axiom/ui";
import { useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { usageText } from "../locales/usage";
import type { HoverTipData } from "./usage-chart-view";
import { useUsagePage } from "./usage-data";

// the route's loader asks for the page's first screen with its code (warm.ts)
export { warmUsage as warm } from "./usage-data";

const phoneQuery = "(max-width: 819px)";

function subscribePhone(listener: () => void) {
  const list = matchMedia(phoneQuery);
  list.addEventListener("change", listener);
  return () => list.removeEventListener("change", listener);
}

const isPhone = () => matchMedia(phoneQuery).matches;

function UsageTipContent({ tip }: { tip: HoverTipData | null }) {
  if (!tip) return null;
  return (
    <div aria-live="polite">
      <b>{tip.bucketLabel}</b>
      <div>
        <span>{tip.totalFormatted}</span>
      </div>
      {tip.items.map((item) => (
        <div key={item.id}>
          <span>{item.name}</span>
          <em>{item.valueFormatted}</em>
        </div>
      ))}
    </div>
  );
}

type UsagePage = ReturnType<typeof useUsagePage>;

function UsageStatusView({ page }: { page: UsagePage }) {
  const txt = (k: Parameters<typeof usageText>[1]) => usageText(page.language, k);
  if (!page.validation.ok) {
    const actionLabel = page.granularity === "hour" ? txt("switchToDay") : txt("switchTo7d");
    const action =
      page.validation.reason === "limit" ? <Button onClick={page.onRecoverRange}>{actionLabel}</Button> : undefined;
    return (
      <div role="alert">
        <EmptyState compact title={page.rangeErrorText ?? ""} action={action} />
      </div>
    );
  }
  if (page.unavailable) {
    return (
      <EmptyState compact title={txt("unavailable")}>
        {txt("unavailableBody")}
      </EmptyState>
    );
  }
  if (page.main.isPending || page.main.isError || !page.main.data) {
    return (
      <DataState query={page.main} compact>
        {() => null}
      </DataState>
    );
  }
  if (!page.hasUsage) {
    return (
      <EmptyState compact title={page.backfillIncomplete ? txt("backfillEmptyTitle") : txt("emptyTitle")}>
        {page.backfillIncomplete ? txt("backfillEmptyBody") : txt("emptyBody")}
      </EmptyState>
    );
  }
  return null;
}

function CurrencyBar({ page }: { page: UsagePage }) {
  if (page.currencies.length <= 1) return null;
  const label = usageText(page.language, "currencyLabel");
  return (
    <fieldset>
      <legend>{label}</legend>
      {page.currencies.map((code) => (
        <Button
          key={code}
          size="sm"
          variant={code === page.currency ? "primary" : "secondary"}
          aria-pressed={code === page.currency}
          onClick={() => page.onSelectCurrency(code)}
        >
          {code}
        </Button>
      ))}
    </fieldset>
  );
}

function UsagePortals({ page, phone }: { page: UsagePage; phone: boolean }) {
  const txt = (k: Parameters<typeof usageText>[1]) => usageText(page.language, k);
  const Cal = phone ? PhoneCalendar : DesktopCalendar;
  const calT = (key: keyof typeof desktopCalZh) =>
    (phone ? (page.english ? phoneCalEn : phoneCalZh) : page.english ? desktopCalEn : desktopCalZh)[key];
  const moreLabel = page.loadingMoreKeys
    ? txt("loadingMoreKeys")
    : page.moreKeysError
      ? txt("retryMoreKeys")
      : txt("loadMoreKeys");
  return (
    <>
      {page.calOpen && page.calendarProps && page.filterBarEl
        ? createPortal(<Cal {...page.calendarProps} t={calT} />, page.filterBarEl)
        : null}
      {page.hasMoreKeys && page.keyPopEl
        ? createPortal(
            <button
              type="button"
              data-a="morek"
              disabled={page.loadingMoreKeys}
              onClick={(e) => {
                e.stopPropagation();
                page.onLoadMoreKeys();
              }}
            >
              {moreLabel}
            </button>,
            page.keyPopEl,
          )
        : null}
      {page.moversStatusNote && page.moversEl
        ? createPortal(
            page.moversError ? (
              <div role="alert">
                <p>{page.moversStatusNote}</p>
                <Button
                  size="sm"
                  variant="secondary"
                  data-a="retrymov"
                  onClick={(e) => {
                    e.stopPropagation();
                    page.onRetryMovers();
                  }}
                >
                  {txt("moversRetry")}
                </Button>
              </div>
            ) : (
              <p role="status">{page.moversStatusNote}</p>
            ),
            page.moversEl,
          )
        : null}
    </>
  );
}

export default function Usage() {
  const { t } = useTranslation();
  const phone = useSyncExternalStore(subscribePhone, isPhone, () => false);
  const page = useUsagePage();
  const titleKey = "title" as const;
  usePageTitle(t(`usageBoard.${titleKey}`));

  const Region = phone ? PhoneUsage : DesktopUsage;
  const regionT = (key: keyof typeof desktopZh) =>
    (phone ? (page.english ? phoneEn : phoneZh) : page.english ? desktopEn : desktopZh)[key];

  return (
    <>
      <CurrencyBar page={page} />
      {page.backfillIncomplete ? <p role="status">{usageText(page.language, "backfillNotice")}</p> : null}
      <Region {...page.usageProps} t={regionT} tip={<UsageTipContent tip={page.hoverTip} />} />
      <UsagePortals page={page} phone={phone} />
      <UsageStatusView page={page} />
    </>
  );
}
