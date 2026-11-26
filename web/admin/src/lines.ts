import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { useInfiniteQuery, useQueries } from "@tanstack/react-query";
import { supplierQuery, suppliersQuery } from "./queries";

type Line = Pick<AdminSchemas["MonitorLine"], "supplier_id" | "group_id">;

export const lineKey = (line: Line) => `${line.supplier_id}:${line.group_id}`;

// What a line is called (ADMIN_HOWTO「线路不再用字母」): the channel's own short name, and the group after a slash only
// where the channel has more than one line to tell apart. The channel's name is the supplier's; its groups are read from
// the supplier, only for a supplier with more than one line in what is shown. A session that may not list suppliers, or a
// line whose supplier has gone, is named by the first part of its group's ID, which still tells two lines apart.
export function useLineNames(lines: readonly Line[]) {
  const allowed = can(useSession(), "suppliers.view");
  const suppliers = useInfiniteQuery({ ...suppliersQuery(), enabled: allowed });
  const names = new Map(suppliers.data?.pages.flatMap((page) => page.data).map((one) => [one.id, one.name] as const));
  const perSupplier = new Map<string, number>();
  for (const line of lines) perSupplier.set(line.supplier_id, (perSupplier.get(line.supplier_id) ?? 0) + 1);
  const several = [...perSupplier].filter(([, count]) => count > 1).map(([id]) => id);
  const details = useQueries({ queries: several.map((id) => ({ ...supplierQuery(id), enabled: allowed })) });
  const groups = new Map(
    details.flatMap((detail) => detail.data?.groups.map((group) => [group.id, group.name] as const) ?? []),
  );
  return (line: Line) => {
    const supplier = names.get(line.supplier_id) ?? line.group_id.slice(0, 8);
    const group = groups.get(line.group_id);
    return group ? `${supplier}/${group}` : supplier;
  };
}
