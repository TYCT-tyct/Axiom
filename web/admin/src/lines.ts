import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { useInfiniteQuery, useQueries } from "@tanstack/react-query";
import { useEffect } from "react";
import { supplierQuery, suppliersQuery } from "./queries";

type Supplier = AdminSchemas["Supplier"];
type Group = AdminSchemas["SupplierGroup"];
type Endpoint = AdminSchemas["SupplierEndpoint"];

// A line is what the design's pages name where a route goes: one group of one supplier (ADMIN_HOWTO「线路不再用字母」).
// It is named by the supplier, with the group added when the supplier has several taken in: "Harbor", "North/vip".
export type Line = {
  groupId: string;
  name: string;
  // What a preset's run-time order names the group by; absent until the group is first taken in.
  codename?: string;
  supplier: Supplier;
  group: Group;
  // Whether a request can go through it now: the supplier syncs, the group is taken in and still listed, and its key
  // is not known to be failing.
  usable: boolean;
};

const syncing: readonly Supplier["status"][] = ["active", "low_balance"];

function lineOf(supplier: Supplier, group: Group, shared: boolean): Line {
  return {
    groupId: group.id,
    name: shared ? `${supplier.name}/${group.name}` : supplier.name,
    codename: group.codename,
    supplier,
    group,
    usable:
      syncing.includes(supplier.status) &&
      group.selected &&
      group.present &&
      group.key_state !== "failed" &&
      group.key_state !== "unavailable",
  };
}

type Detail = Pick<AdminSchemas["SupplierDetail"], "supplier" | "groups"> & { endpoints?: Endpoint[] };

// linesOf names the lines of the suppliers that were read: a group that was never taken in is no line, and a supplier
// with more than one taken in names each with its group.
export function linesOf(details: readonly Detail[]): Line[] {
  return details.flatMap(({ supplier, groups }) => {
    const taken = groups.filter((group) => group.selected);
    return taken.map((group) => lineOf(supplier, group, taken.length > 1));
  });
}

export type Lines = {
  all: Line[];
  byGroup: ReadonlyMap<string, Line>;
  byCodename: ReadonlyMap<string, Line>;
  // The endpoints a model has, each with the line it runs on (absent when its group is not a line).
  endpointsOf: (modelId: string) => { endpoint: Endpoint; line: Line | undefined }[];
  // False while the suppliers are being read: a count then is not yet the count.
  ready: boolean;
  // False without the permission to read suppliers: the pages say nothing of lines.
  allowed: boolean;
};

// useLines reads the suppliers and what each serves, once for all pages that name lines (the queries are shared). A
// supplier whose detail cannot be read gives no lines, and the page shows what it has rather than failing.
export function useLines(): Lines {
  const allowed = can(useSession(), "suppliers.view");
  const list = useInfiniteQuery({ ...suppliersQuery(), enabled: allowed });
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = list;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);
  const suppliers = list.data?.pages.flatMap((page) => page.data) ?? [];
  const details = useQueries({
    queries: suppliers.map((supplier) => ({ ...supplierQuery(supplier.id), enabled: allowed })),
  });
  const read = details.flatMap((detail) => (detail.data ? [detail.data] : []));
  const all = linesOf(read);
  const byGroup = new Map(all.map((line) => [line.groupId, line]));
  const byCodename = new Map(all.flatMap((line) => (line.codename ? [[line.codename, line] as const] : [])));
  const endpoints = read.flatMap((detail) => detail.endpoints ?? []);
  return {
    all,
    byGroup,
    byCodename,
    endpointsOf: (modelId) =>
      endpoints
        .filter((endpoint) => endpoint.model_id === modelId)
        .map((endpoint) => ({ endpoint, line: byGroup.get(endpoint.group_id) })),
    ready: allowed && !list.isPending && !hasNextPage && details.every((detail) => !detail.isPending),
    allowed,
  };
}

type LineRef = Pick<AdminSchemas["MonitorLine"], "supplier_id" | "group_id">;

export const lineKey = (line: LineRef) => `${line.supplier_id}:${line.group_id}`;

// useLineNames names the lines a page shows by the one rule above (useLines), so a line is called the same on every page.
// A session that may not read suppliers, or a line whose supplier has gone, is named by the first part of its group's ID,
// which still tells two lines apart.
export function useLineNames(): (line: LineRef) => string {
  const { byGroup } = useLines();
  return (line) => byGroup.get(line.group_id)?.name ?? line.group_id.slice(0, 8);
}
