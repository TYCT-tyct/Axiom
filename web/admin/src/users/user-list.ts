import type { AdminSchemas } from "@axiom/api";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { usersQuery } from "../queries";
import { useAddressParam } from "../row-drawer";

export type User = AdminSchemas["UserSummary"];
export type UserFilter = "all" | "active" | "off";
export const userFilters = ["all", "active", "off"] as const satisfies readonly UserFilter[];

// Whether a user may sign in: the sign-in switch is the user's, and a node of the release before does not give it, in
// which case the user is taken as allowed to.
export const signInOff = (user: User) => user.sign_in_disabled === true;

const passes: Record<UserFilter, (user: User) => boolean> = {
  all: () => true,
  active: (user) => user.status === "active" && !signInOff(user),
  off: (user) => user.status !== "active" || signInOff(user),
};

// The users of the site that a search by the start of the email finds, which the users, accounts and roles tabs all list:
// there is no operation that lists accounts or roles, so a person is found first and the account and the roles are
// reached from them. The search is the address's (q); the filter by state narrows what has been loaded.
export function useUserList() {
  const { address: q, text, setText } = useAddressParam("q");
  const [filter, setFilter] = useState<UserFilter>("all");
  const query = useInfiniteQuery(usersQuery({ email_prefix: q }));
  const loaded = query.data?.pages.flatMap((page) => page.data) ?? [];
  const rows = loaded.filter(passes[filter]);
  return { q, text, setText, filter, setFilter, query, loaded, rows };
}
