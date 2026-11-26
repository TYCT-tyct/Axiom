import { ActionButton, Cluster, DataTable, Stack, Text, Tooltip } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import type { Line, Lines } from "../lines";
import { Code } from "../parts";
import { add, move, remove } from "./line-order";

type Entry = { code: string; at: number; line: Line | undefined };

// Order is a preset's line order: the lines in the order the router tries them, each with what it is now, and, where the
// person may change it, up, down, take away, and the lines still to add. A preset keeps at least one line once it has an
// order.
export function Order({
  order,
  lines,
  editable,
  free,
  hold,
  onChange,
}: {
  order: readonly string[];
  lines: Lines;
  editable: boolean;
  free: readonly Line[];
  hold: boolean;
  onChange: (order: readonly string[]) => void;
}) {
  const { t } = useTranslation();
  if (order.length === 0 && !editable) {
    return <Text tone="muted">{t("routingPresets.order.none")}</Text>;
  }
  return (
    <Stack gap={8}>
      <Text as="h3" medium>
        {t("routingPresets.order.title")}
      </Text>
      {order.length === 0 ? (
        <Text tone="muted">{t("routingPresets.order.noneStart")}</Text>
      ) : (
        <OrderTable
          entries={order.map((code, at) => ({ code, at, line: lines.byCodename.get(code) }))}
          order={order}
          editable={editable}
          hold={hold}
          onChange={onChange}
        />
      )}
      {editable && free.length > 0 ? (
        <AddLines free={free} hold={hold} onAdd={(code) => onChange(add(order, code))} />
      ) : null}
    </Stack>
  );
}

function note(t: (key: `routingPresets.order.${"unknown" | "stopped" | "first" | "next"}`) => string, entry: Entry) {
  if (!entry.line) return t("routingPresets.order.unknown");
  if (!entry.line.usable) return t("routingPresets.order.stopped");
  return entry.at === 0 ? t("routingPresets.order.first") : t("routingPresets.order.next");
}

function OrderTable({
  entries,
  order,
  editable,
  hold,
  onChange,
}: {
  entries: readonly Entry[];
  order: readonly string[];
  editable: boolean;
  hold: boolean;
  onChange: (order: readonly string[]) => void;
}) {
  const { t } = useTranslation();
  return (
    <DataTable
      label={t("routingPresets.order.title")}
      rows={entries}
      rowKey={(entry) => entry.code}
      columns={[
        { key: "at", header: t("routingPresets.order.at"), align: "end", cell: (entry) => entry.at + 1 },
        {
          key: "line",
          header: t("routingPresets.order.line"),
          cell: (entry) => (
            <Stack gap={4}>
              <Text as="span">{entry.line?.name ?? entry.code}</Text>
              <Code>{entry.code}</Code>
            </Stack>
          ),
        },
        { key: "note", header: t("routingPresets.order.note"), muted: true, cell: (entry) => note(t, entry) },
        ...(editable
          ? [
              {
                key: "move",
                header: "",
                align: "end" as const,
                cell: (entry: Entry) => <Moves entry={entry} order={order} hold={hold} onChange={onChange} />,
              },
            ]
          : []),
      ]}
    />
  );
}

function Moves({
  entry,
  order,
  hold,
  onChange,
}: {
  entry: Entry;
  order: readonly string[];
  hold: boolean;
  onChange: (order: readonly string[]) => void;
}) {
  const { t } = useTranslation();
  const name = entry.line?.name ?? entry.code;
  return (
    <Cluster gap={4} justify="end">
      <ActionButton
        variant="gho"
        size="sm"
        icon="up"
        aria-label={t("routingPresets.order.up", { name })}
        disabled={hold || entry.at === 0}
        onClick={() => onChange(move(order, entry.at, -1))}
      />
      <ActionButton
        variant="gho"
        size="sm"
        icon="down"
        aria-label={t("routingPresets.order.down", { name })}
        disabled={hold || entry.at === order.length - 1}
        onClick={() => onChange(move(order, entry.at, 1))}
      />
      <ActionButton
        variant="gho"
        size="sm"
        icon="close"
        aria-label={t("routingPresets.order.remove", { name })}
        disabled={hold || order.length === 1}
        onClick={() => onChange(remove(order, entry.at))}
      />
    </Cluster>
  );
}

function AddLines({ free, hold, onAdd }: { free: readonly Line[]; hold: boolean; onAdd: (code: string) => void }) {
  const { t } = useTranslation();
  return (
    <Cluster gap={8}>
      <Text as="span" tone="muted">
        {t("routingPresets.order.add")}
      </Text>
      {free.map((line) => (
        <Tooltip key={line.groupId} text={line.supplier.name}>
          <ActionButton
            size="sm"
            disabled={hold}
            aria-label={t("routingPresets.order.addNamed", { name: line.name })}
            onClick={() => onAdd(line.codename ?? "")}
          >
            {line.name}
          </ActionButton>
        </Tooltip>
      ))}
    </Cluster>
  );
}
