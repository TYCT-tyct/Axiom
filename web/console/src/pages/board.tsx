import { DataState, EmptyState } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import { type KeyboardEvent as KeyEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// What the pages ported from the AxApp board share. The board comes in two forms, the wide one and the phone's, each a
// region of its own, so the page renders the one the window is wide enough for: from 1024px the wide board, as the shell
// switches too (the board's own container queries then measure the frame).
export function useWide() {
  const [wide, setWide] = useState(true);
  useEffect(() => {
    const query = window.matchMedia("(min-width: 1024px)");
    const update = () => setWide(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);
  return wide;
}

// A board element the page makes interactive — a row's name, which the board draws as text — takes a button's keys and
// keeps its look, as the shell's menu entries do.
export function pressKey(event: KeyEvent<Element>) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    (event.currentTarget as HTMLElement).click();
  }
}

// ListStates is a list's four states in the page's own words: no permission to read it, a read that failed with its
// reason and a retry, or nothing yet. The board's own markup keeps its place meanwhile, as the overview's cards do.
export function ListStates({
  query,
  allowed,
  ns,
}: {
  query: UseQueryResult<{ data: unknown[] }>;
  allowed: boolean;
  ns: "routingPresets" | "providerKeys";
}) {
  const { t } = useTranslation();
  const text = (key: string) => t(key as never) as string;
  if (!allowed) {
    return <EmptyState title={text(`${ns}.noPermission.title`)}>{text(`${ns}.noPermission.body`)}</EmptyState>;
  }
  if (query.isError) {
    return (
      <DataState query={query} compact>
        {() => null}
      </DataState>
    );
  }
  return query.data && query.data.data.length === 0 ? (
    <EmptyState title={text(`${ns}.empty.title`)}>{text(`${ns}.empty.body`)}</EmptyState>
  ) : null;
}
