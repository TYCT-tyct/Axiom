import type { AdminSchemas } from "@axiom/api";
import { Button, SelectField, TextField } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { isProblem, type Problem } from "./change";
import { billingItems } from "./models/catalog";

export type Rules = AdminSchemas["SourceClassRules"];
type Condition = AdminSchemas["SourceClassCondition"];

type Row = {
  key: number;
  evidence: Condition["evidence"];
  name: string;
  item: string;
  operator: string;
  value: string;
};

const headerOperators = ["present", "absent", "equals", "prefix", "suffix", "contains"] as const;
const usageOperators = ["present", "absent"] as const;

// ConditionRows edits the rule list. It owns the rows because adding and removing one changes the form; every input
// still has a name, so the dialog reads the same form values as every other change. The count is a hidden field: an
// empty list has no row inputs, and the reader must know it is empty rather than stale.
export function ConditionRows({ initial }: { initial?: Rules }) {
  const { t } = useTranslation();
  const [match, setMatch] = useState<Rules["match"]>(initial?.match ?? "all");
  const [rows, setRows] = useState<Row[]>(() =>
    (initial?.conditions ?? []).map((condition, key) => ({
      key,
      evidence: condition.evidence,
      name: condition.name ?? "",
      item: condition.item ?? "input",
      operator: condition.operator,
      value: condition.value ?? "",
    })),
  );
  const nextKey = useRef((initial?.conditions.length ?? 0) + 1);
  const change = (key: number, patch: Partial<Row>) =>
    setRows((current) => current.map((row) => (row.key === key ? { ...row, ...patch } : row)));
  const add = () =>
    setRows((current) => [
      ...current,
      { key: nextKey.current++, evidence: "header", name: "", item: "input", operator: "present", value: "" },
    ]);
  const remove = (key: number) => setRows((current) => current.filter((row) => row.key !== key));
  return (
    <div className="space-y-3">
      <input type="hidden" name="count" value={rows.length} />
      <SelectField
        label={t("sourceClasses.match")}
        name="match"
        value={match}
        onChange={(event) => setMatch(event.target.value as Rules["match"])}
      >
        <option value="all">{t("sourceClasses.all")}</option>
        <option value="any">{t("sourceClasses.any")}</option>
      </SelectField>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-ui font-medium text-t1">{t("sourceClasses.conditions")}</h3>
        <Button size="sm" onClick={add} disabled={rows.length >= 8}>
          {t("sourceClasses.addCondition")}
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="text-label text-t3">{t("sourceClasses.noConditions")}</p>
      ) : (
        rows.map((row, index) => (
          <ConditionRow
            key={row.key}
            row={row}
            index={index}
            onChange={(patch) => change(row.key, patch)}
            onRemove={() => remove(row.key)}
          />
        ))
      )}
    </div>
  );
}

// ConditionRow is one condition. Its inputs are named by position, not by key, so the reader walks 0..count.
function ConditionRow({
  row,
  index,
  onChange,
  onRemove,
}: {
  row: Row;
  index: number;
  onChange: (patch: Partial<Row>) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  return (
    <fieldset className="space-y-3 rounded-btn border border-b1 p-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <SelectField
          label={t("sourceClasses.evidence")}
          name={`evidence-${index}`}
          value={row.evidence}
          onChange={(event) => {
            const evidence = event.target.value as Condition["evidence"];
            onChange({ evidence, operator: "present", name: "", value: "" });
          }}
        >
          <option value="header">{t("sourceClasses.header")}</option>
          <option value="usage">{t("sourceClasses.usage")}</option>
        </SelectField>
        <SubjectField row={row} index={index} onChange={onChange} />
        <SelectField
          label={t("sourceClasses.operator")}
          name={`operator-${index}`}
          value={row.operator}
          onChange={(event) => onChange({ operator: event.target.value, value: "" })}
        >
          {(row.evidence === "header" ? headerOperators : usageOperators).map((operator) => (
            <option key={operator} value={operator}>
              {t(`sourceClasses.operators.${operator}`)}
            </option>
          ))}
        </SelectField>
        {row.operator === "present" || row.operator === "absent" ? null : (
          <TextField
            label={t("sourceClasses.value")}
            hint={t("sourceClasses.valueHint")}
            name={`value-${index}`}
            value={row.value}
            onChange={(event) => onChange({ value: event.target.value })}
            maxLength={256}
          />
        )}
      </div>
      <div className="flex justify-end">
        <Button size="sm" onClick={onRemove}>
          {t("sourceClasses.removeCondition")}
        </Button>
      </div>
    </fieldset>
  );
}

// SubjectField names what a condition looks at: a response header by name, or a usage item from the catalog's list.
function SubjectField({ row, index, onChange }: { row: Row; index: number; onChange: (patch: Partial<Row>) => void }) {
  const { t } = useTranslation();
  return row.evidence === "header" ? (
    <TextField
      label={t("sourceClasses.headerName")}
      hint={t("sourceClasses.headerNameHint")}
      name={`name-${index}`}
      value={row.name}
      onChange={(event) => onChange({ name: event.target.value })}
      maxLength={256}
    />
  ) : (
    <SelectField
      label={t("sourceClasses.item")}
      name={`item-${index}`}
      value={row.item}
      onChange={(event) => onChange({ item: event.target.value })}
    >
      {billingItems.map((item) => (
        <option key={item} value={item}>
          {t(`models.item.${item}`)}
        </option>
      ))}
    </SelectField>
  );
}

export function readRules(values: Record<string, string>, t: TFunction<"admin">): Rules | Problem {
  const count = Number(values.count ?? 0);
  if (!Number.isInteger(count) || count < 0 || count > 8) {
    return { problem: t("sourceClasses.invalidCount") };
  }
  const conditions: Condition[] = [];
  for (let index = 0; index < count; index++) {
    const condition = readCondition(values, index, t);
    if (isProblem(condition)) {
      return condition;
    }
    conditions.push(condition);
  }
  return { match: values.match === "any" ? "any" : "all", conditions };
}

function readCondition(values: Record<string, string>, index: number, t: TFunction<"admin">): Condition | Problem {
  const evidence = (values[`evidence-${index}`] === "usage" ? "usage" : "header") as Condition["evidence"];
  const chosen = values[`operator-${index}`] ?? "present";
  if (evidence === "usage") {
    return readUsageCondition(values, index, chosen, t);
  }
  return readHeaderCondition(values, index, chosen, t);
}

function readUsageCondition(
  values: Record<string, string>,
  index: number,
  chosen: string,
  t: TFunction<"admin">,
): Condition | Problem {
  const item = values[`item-${index}`] ?? "";
  if (item === "") {
    return { problem: t("sourceClasses.invalidConditionValue") };
  }
  const operator = (chosen === "absent" ? "absent" : "present") as Condition["operator"];
  return { evidence: "usage", item, operator };
}

function readHeaderCondition(
  values: Record<string, string>,
  index: number,
  operator: string,
  t: TFunction<"admin">,
): Condition | Problem {
  const name = values[`name-${index}`]?.trim() ?? "";
  if (name === "") {
    return { problem: t("sourceClasses.invalidConditionName") };
  }
  const condition: Condition = { evidence: "header", name, operator: operator as Condition["operator"] };
  if (operator === "present" || operator === "absent") {
    return condition;
  }
  const value = values[`value-${index}`] ?? "";
  if (value === "" || value.length > 256) {
    return { problem: t("sourceClasses.invalidConditionValue") };
  }
  condition.value = value;
  return condition;
}
