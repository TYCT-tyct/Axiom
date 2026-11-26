import type { AdminSchemas } from "@axiom/api";
import type { DataTable } from "@axiom/ui";
import { Id, useFormat } from "@axiom/ui";
import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";

type Call = AdminSchemas["AdminCallSummary"];
type Column = ComponentProps<typeof DataTable<Call>>["columns"][number];

// The columns every list of calls begins with, in the order the lists show them: when (the button that opens the call),
// the request, the account and the model. A narrow window keeps the time and the model, and a list may set a column apart. The keys are the ones a list
// takes its columns by, under the words of the calls page (calls.col) whichever page lists them.
export function useCallColumns(): Record<"at" | "id" | "account" | "model", Column> {
  const { t } = useTranslation();
  const format = useFormat();
  return {
    at: { key: "at", header: t("calls.col.at"), cell: (call) => (call.at ? format.dateTime(new Date(call.at)) : "—") },
    id: { key: "id", header: t("calls.col.id"), from: "md", cell: (call) => (call.id ? <Id value={call.id} /> : "—") },
    account: {
      key: "account",
      header: t("calls.col.account"),
      from: "lg",
      cell: (call) => (call.account_id ? <Id value={call.account_id} /> : "—"),
    },
    model: { key: "model", header: t("calls.col.model"), cell: (call) => call.model ?? "—" },
  };
}
