import { DataState, EmptyState } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { wideWidth } from "./board-interaction";

export { pressBoardButton as pressKey } from "./board-interaction";

// What the console's pages ported from the AxApp board share. The board comes in two forms, the wide one and the phone's,
// each a region of its own, so a page renders the one the window is wide enough for: from 1024px the wide board, as the
// shell switches too (the board's own container queries then measure the frame). The width is read as the page first
// renders, not in an effect: the form may not change under a page that just took focus, which is how a phone shows
// itself as the wide board for a moment and the focus lands on a heading that is then replaced.
export function useWide() {
  return useSyncExternalStore(wideWidth.subscribe, wideWidth.matches, () => true);
}

// ListStates is a list's four states in the page's own words: loading, no permission to read it, a failed read with its
// reason and a retry (a node that refuses the read is a forbidden state, with its request ID), or nothing yet. The
// board's own markup keeps its place meanwhile, as the overview's cards do.
export function ListStates({
  query,
  view,
  ns,
}: {
  query: UseQueryResult<{ data: unknown[] }>;
  view: boolean;
  ns: "routingPresets" | "providerKeys";
}) {
  const { t } = useTranslation();
  const text = (key: string) => t(key as never) as string;
  if (!view) {
    return <EmptyState title={text(`${ns}.noPermission.title`)}>{text(`${ns}.noPermission.body`)}</EmptyState>;
  }
  return (
    <DataState
      query={query}
      compact
      isEmpty={(data) => data.data.length === 0}
      empty={<EmptyState title={text(`${ns}.empty.title`)}>{text(`${ns}.empty.body`)}</EmptyState>}
    >
      {() => null}
    </DataState>
  );
}
