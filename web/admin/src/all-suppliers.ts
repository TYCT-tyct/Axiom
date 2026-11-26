import type { AdminSchemas } from "@axiom/api";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { suppliersQuery } from "./queries";

// useAllSuppliers reads every page of the supplier list, for what needs them all rather than a page to show: a picker,
// a count, the names of the lines. ready is false until the last page is in. A page that lists suppliers for the person
// to read keeps its own paging (the channels tab).
export function useAllSuppliers(enabled = true) {
  const list = useInfiniteQuery({ ...suppliersQuery(), enabled });
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = list;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);
  const suppliers: AdminSchemas["Supplier"][] = list.data?.pages.flatMap((page) => page.data) ?? [];
  // query is the list's own state, for a page that shows it loading or failing (DataState).
  return { suppliers, ready: enabled && !list.isPending && !hasNextPage, query: list };
}
