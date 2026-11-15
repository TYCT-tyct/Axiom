import { ApiError } from "@axiom/api";
import { type ReactNode, useState } from "react";
import { glyphNames } from "./components/glyph";
import {
  ActionButton,
  Avatar,
  Card,
  Cluster,
  CreateDialog,
  DataState,
  DataTable,
  EditPanel,
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
  Sheet,
  Stack,
  StateBlock,
  SubmitFailed,
  Switch,
  Tag,
  TextArea,
  ToastProvider,
  useExplicitSave,
  useLiveSave,
  useToast,
  useUndo,
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
  createLede: "Key 只在创建后显示一次。",
  created: "已创建 ci-runner",
  shownOnce: "现在复制它，关掉之后就看不到了。",
  edit: "编辑 key",
  editLive: "通知",
  balance: "余额不足时通知我",
  digest: "每周发一封用量汇总",
  sheet: "筛选",
  saved: "已保存",
  disabled: "已停用 ci-runner",
  failed: "没保存成功",
  stateTitle: "还没有 key",
  stateBody: "建一把 key，就能在你的工具里调用模型。",
} as const;

// Opener renders a button that mounts an overlay, which unmounts it again when it closes.
function Opener({ label, overlay }: { label: string; overlay: (close: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <ActionButton onClick={() => setOpen(true)}>{label}</ActionButton>
      {open ? overlay(() => setOpen(false)) : null}
    </>
  );
}

// A fake API for the demos: it answers after a moment with what it was sent.
const answer = <T,>(value: T) => new Promise<T>((resolve) => setTimeout(() => resolve(value), 600));

function ExplicitDemo({ close }: { close: () => void }) {
  const write = useExplicitSave({ initial: { name: "ci-runner" }, save: answer });
  return (
    <EditPanel title={text.edit} subtitle={rows[0]?.id} write={write} onClose={close}>
      <Field label={text.name} value={write.draft.name} onChange={(e) => write.set({ name: e.target.value })} />
    </EditPanel>
  );
}

function LiveDemo({ close }: { close: () => void }) {
  const write = useLiveSave({ initial: { balance: true, digest: false }, write: answer });
  return (
    <EditPanel title={text.editLive} write={write} onClose={close}>
      <Stack gap={12}>
        <Switch
          label={text.balance}
          checked={write.value.balance}
          onChange={(balance) => write.change((p) => ({ ...p, balance }))}
        />
        <Switch
          label={text.digest}
          checked={write.value.digest}
          onChange={(digest) => write.change((p) => ({ ...p, digest }))}
        />
      </Stack>
    </EditPanel>
  );
}

function ToastDemo() {
  const say = useToast();
  const undo = useUndo();
  return (
    <Stack>
      <Cluster>
        <ActionButton onClick={() => say(text.saved)}>{text.saved}</ActionButton>
        <ActionButton onClick={() => say(text.failed, { bad: true })}>{text.failed}</ActionButton>
        <ActionButton onClick={() => undo.offer(text.disabled, () => answer(null))}>{text.disabled}</ActionButton>
      </Cluster>
      {undo.failed}
    </Stack>
  );
}

const query = <T,>(status: "pending" | "error" | "success", data?: T, error: unknown = null) => ({
  status,
  data,
  error,
  errorUpdatedAt: Date.UTC(2026, 9, 24, 2, 30),
  refetch: () => {},
});

const failure = new ApiError(503, "req_k7Hc2pQ9xWm4");

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
  "create-dialog": () => (
    <Opener
      label={text.create}
      overlay={(close) => (
        <CreateDialog
          title={text.create}
          subtitle={text.createLede}
          initial={{ name: "" }}
          create={(draft) => answer(`sk-ax-v1-${draft.name}-7Hc2pQ9xWm4k`)}
          result={(secret) => ({ title: text.created, subtitle: text.shownOnce, body: <Id value={secret} /> })}
          onClose={close}
        >
          {(draft, set) => (
            <Field label={text.name} value={draft.name} onChange={(e) => set({ name: e.target.value })} />
          )}
        </CreateDialog>
      )}
    />
  ),
  "edit-panel": () => (
    <Cluster>
      <Opener label={text.edit} overlay={(close) => <ExplicitDemo close={close} />} />
      <Opener label={text.editLive} overlay={(close) => <LiveDemo close={close} />} />
    </Cluster>
  ),
  sheet: () => (
    <Opener
      label={text.sheet}
      overlay={(close) => (
        <Sheet
          title={text.sheet}
          dismissible
          onClose={close}
          footer={(leave) => (
            <ActionButton variant="pri" onClick={leave}>
              {text.sheet}
            </ActionButton>
          )}
        >
          <SegmentDemo />
        </Sheet>
      )}
    />
  ),
  toast: () => (
    <ToastProvider>
      <ToastDemo />
    </ToastProvider>
  ),
  "submit-failed": () => <SubmitFailed error={failure} />,
  "state-block": () => (
    <Grid columns={2}>
      <StateBlock
        icon="keys"
        title={text.stateTitle}
        level={2}
        compact
        actions={
          <ActionButton variant="pri" icon="plus">
            {text.create}
          </ActionButton>
        }
      >
        {text.stateBody}
      </StateBlock>
      <DataState query={query<string>("error", undefined, failure)} compact>
        {(data) => data}
      </DataState>
      <DataState query={query<string>("error", undefined, new ApiError(403, null))} compact>
        {(data) => data}
      </DataState>
      <DataState query={query<string>("pending")}>{(data) => data}</DataState>
    </Grid>
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
