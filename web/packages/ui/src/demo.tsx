import { type ReactNode, useState } from "react";
import { glyphNames } from "./components/glyph";
import {
  ActionButton,
  Avatar,
  Card,
  Cluster,
  DataTable,
  Field,
  Glyph,
  Grid,
  Id,
  Menu,
  Note,
  PageHead,
  Popover,
  SegmentedControl,
  Select,
  SettingRow,
  Stack,
  Switch,
  Tag,
  TextArea,
} from "./index";

// The primitives in their variants, one demo per component, for the screenshot comparison against AxSpec's component
// area (port-design F1c) and for the render test. Only sample words, never data a page would show; not shipped in any
// app.
const text = {
  page: "组件",
  lede: "基础层的每个组件和它的变体。",
  create: "新建 key",
  export: "导出",
  cancel: "取消",
  remove: "删除",
  more: "更多",
  card: "调用额度",
  cardLede: "每个周期重新计算。",
  row: "允许调用",
  rowDetail: "关闭后这把 key 的请求会被拒绝。",
  name: "名称",
  nameHint: "只有你能看到。",
  nameError: "名称已被使用",
  note: "请写下调用这把 key 的工具",
  period: "周期",
  noteTitle: "余额不足 10 元",
  noteDetail: "调用会在余额用完时停止。",
  table: "最近的调用",
  model: "模型",
  id: "ID",
  cost: "费用",
  language: "语言",
  segment: "范围",
  day: "天",
  week: "周",
  month: "月",
} as const;

const rows = [
  { id: "call_01JD8Z5Q7W3M2K9X4C6V8B0N1P", model: "deepseek-v4", cost: "¥0.0123" },
  { id: "call_01JD8Z5Q7W3M2K9X4C6V8B0N2Q", model: "claude-sonnet-5-5", cost: "¥1.2000" },
];

function SegmentDemo() {
  const [value, setValue] = useState<"day" | "week" | "month">("week");
  const options = (["day", "week", "month"] as const).map((v) => ({ value: v, label: text[v] }));
  return <SegmentedControl label={text.segment} value={value} options={options} onChange={setValue} />;
}

function SwitchDemo() {
  const [on, setOn] = useState(true);
  return (
    <Cluster>
      <Switch label={text.row} checked={on} onChange={setOn} />
      <Switch label={text.row} checked={false} disabled onChange={() => {}} />
    </Cluster>
  );
}

export const demos: Record<string, () => ReactNode> = {
  icon: () => (
    <Stack gap={12}>
      <Cluster gap={12}>
        {glyphNames.map((name) => (
          <Glyph key={name} name={name} size={20} />
        ))}
      </Cluster>
      <Cluster gap={12}>
        {glyphNames.map((name) => (
          <Glyph key={name} name={name} size={20} selected />
        ))}
      </Cluster>
    </Stack>
  ),
  "page-head": () => (
    <PageHead title={text.page} lede={text.lede} actions={<ActionButton>{text.export}</ActionButton>} />
  ),
  button: () => (
    <Cluster>
      <ActionButton variant="pri" icon="plus">
        {text.create}
      </ActionButton>
      <ActionButton icon="export">{text.export}</ActionButton>
      <ActionButton variant="gho">{text.cancel}</ActionButton>
      <ActionButton variant="dan">{text.remove}</ActionButton>
      <ActionButton size="sm">{text.export}</ActionButton>
      <ActionButton variant="pri" disabled>
        {text.create}
      </ActionButton>
    </Cluster>
  ),
  card: () => (
    <Grid columns={2}>
      <Card title={text.card} lede={text.cardLede} actions={<ActionButton size="sm">{text.export}</ActionButton>}>
        <Tag>{text.month}</Tag>
      </Card>
      <Card title={text.remove} danger>
        <ActionButton variant="dan">{text.remove}</ActionButton>
      </Card>
    </Grid>
  ),
  "setting-row": () => (
    <Card layout="rows">
      <SettingRow title={text.row} detail={text.rowDetail}>
        <SwitchDemo />
      </SettingRow>
      <SettingRow title={text.period}>
        <SegmentDemo />
      </SettingRow>
    </Card>
  ),
  table: () => (
    <Card layout="flush" title={text.table}>
      <DataTable
        label={text.table}
        fixed
        rows={rows}
        rowKey={(row) => row.id}
        columns={[
          { key: "id", header: text.id, width: 160, cell: (row) => <Id value={row.id} /> },
          { key: "model", header: text.model, width: 200, cell: (row) => row.model },
          { key: "cost", header: text.cost, width: 120, align: "end", cell: (row) => row.cost },
        ]}
      />
    </Card>
  ),
  segmented: () => <SegmentDemo />,
  switch: () => <SwitchDemo />,
  tag: () => (
    <Cluster>
      <Tag>{text.day}</Tag>
      <Tag tone="ok">{text.day}</Tag>
      <Tag tone="warn">{text.week}</Tag>
      <Tag tone="bad">{text.month}</Tag>
      <Tag tone="accent">{text.month}</Tag>
    </Cluster>
  ),
  avatar: () => (
    <Cluster>
      <Avatar name="axiom" size="sm" />
      <Avatar name="王小明" />
      <Avatar name="Zoë" size="lg" />
    </Cluster>
  ),
  field: () => (
    <Grid columns={3}>
      <Field label={text.name} hint={text.nameHint} placeholder={text.name} />
      <Field label={text.name} error={text.nameError} defaultValue="ci" />
      <Select
        label={text.period}
        options={(["day", "week", "month"] as const).map((v) => ({ value: v, label: text[v] }))}
      />
      <TextArea label={text.note} />
    </Grid>
  ),
  note: () => (
    <Note title={text.noteTitle} action={<ActionButton size="sm">{text.export}</ActionButton>}>
      {text.noteDetail}
    </Note>
  ),
  popover: () => (
    <Cluster>
      <Menu
        label={text.more}
        trigger={(props) => (
          <ActionButton {...props} variant="gho" icon="more">
            {text.more}
          </ActionButton>
        )}
        items={[
          { label: text.export, icon: "export", onSelect: () => {} },
          { label: text.remove, icon: "close", danger: true, onSelect: () => {} },
        ]}
      />
      <Popover label={text.language} trigger={(props) => <ActionButton {...props}>{text.language}</ActionButton>}>
        {(close) => <ActionButton onClick={close}>{text.cancel}</ActionButton>}
      </Popover>
    </Cluster>
  ),
};

export function ComponentDemo() {
  return (
    <Stack gap={32}>
      {Object.entries(demos).map(([name, demo]) => (
        <section key={name} data-demo={name}>
          {demo()}
        </section>
      ))}
    </Stack>
  );
}
