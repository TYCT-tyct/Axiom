import { Badge, Button } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// A key's text is never shown again after it is made; the list shows where it starts and ends.
export const maskedKey = (key: { prefix: string; last4: string }) => `${key.prefix}…${key.last4}`;

// LoadMore asks for the next page of a list, which is only there while the last page gave a cursor.
export function LoadMore({
  list,
}: {
  list: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown };
}) {
  const { t } = useTranslation();
  if (!list.hasNextPage) {
    return null;
  }
  return (
    <div className="border-t border-b1 p-3 text-center">
      <Button onClick={() => list.fetchNextPage()} disabled={list.isFetchingNextPage}>
        {list.isFetchingNextPage ? t("loadingMore") : t("loadMore")}
      </Button>
    </div>
  );
}

// How a call ended, in words with an icon. In a crowded row (compact) a phone shows the icon alone and leaves the words
// to a screen reader; the two icons differ in shape, so the colour is still not the only signal.
export function CallStatus({ errorKind, compact = false }: { errorKind: string; compact?: boolean }) {
  const { t } = useTranslation();
  const failed = errorKind !== "";
  return (
    <Badge tone={failed ? "bad" : "ok"} icon={failed ? "alert" : "check"}>
      <span className={compact ? "sr-only sm:not-sr-only" : undefined}>
        {failed ? t("calls.failed") : t("calls.ok")}
      </span>
    </Badge>
  );
}
