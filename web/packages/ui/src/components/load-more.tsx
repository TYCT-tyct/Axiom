import { useTranslation } from "react-i18next";
import { ActionButton } from "./action-button";
import s from "./load-more.module.css";

// LoadMore asks for the next page of a list, which is only there while the last page gave a cursor.
export function LoadMore({
  list,
}: {
  list: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown };
}) {
  const { t } = useTranslation("common");
  if (!list.hasNextPage) {
    return null;
  }
  return (
    <div className={s.root}>
      <ActionButton size="sm" pending={list.isFetchingNextPage} onClick={() => list.fetchNextPage()}>
        {list.isFetchingNextPage ? t("loadingMore") : t("loadMore")}
      </ActionButton>
    </div>
  );
}
