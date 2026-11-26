import { Avatar, Cluster } from "@axiom/ui";
import type { TFunction } from "i18next";
import type { Columns, Format } from "../list-table";
import { shortId } from "../orders/states";
import { Code, State } from "../parts";
import { signInOff, type User } from "./user-list";

// The columns the users, accounts and roles tabs have in common, each with the words its tab gives the header.
type Column = Columns<User>[number];

// The person: a name to see them by, and a tag when they cannot sign in.
export const personColumn = (t: TFunction, header: string, tag = false): Column => ({
  key: "user",
  header,
  cell: (user) => (
    <Cluster>
      <Avatar name={user.email} size="sm" />
      {user.email}
      {tag && signInOff(user) ? <State tone="warn">{t("users.signInOff")}</State> : null}
    </Cluster>
  ),
});

export const accountColumn = (t: TFunction): Column => ({
  key: "account",
  header: t("users.col.account"),
  cell: (user) => <Code title={user.account_id}>{shortId(user.account_id)}</Code>,
});

export const tierColumn = (t: TFunction): Column => ({
  key: "tier",
  header: t("users.col.tier"),
  cell: (user) => user.tier.name,
});

export const createdColumn = (t: TFunction, format: Format): Column => ({
  key: "created",
  header: t("users.col.created"),
  muted: true,
  cell: (user) => format.dateTime(new Date(user.created_at)),
  sort: (user) => Date.parse(user.created_at),
});

// Whether the user is in good standing: active, and allowed to sign in.
export const standingColumn = (t: TFunction): Column => ({
  key: "status",
  header: t("users.col.status"),
  cell: (user) => {
    const good = user.status === "active" && !signInOff(user);
    return <State tone={good ? "good" : "warn"}>{t(good ? "users.active" : "users.signInOff")}</State>;
  },
});
