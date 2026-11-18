// The workspaces page's messages, and the workspace parts of the keys page and settings. They live in their own module
// because the English root locale file is at the code-line limit in scripts/limits.ts; a label and its translation are
// edited as a pair, so both languages stay together.
import type { Messages } from "@axiom/ui";

export const zh = {
  title: "工作区",
  description: "把 key 分开管理，例如生产和测试：每个工作区有自己的 key 和预算。",
  create: "新建工作区",
  default: "默认",
  keys: "查看 key",
  keysNamed: "查看 key：{{name}}",
  unavailable: {
    title: "工作区暂不可用",
    body: "当前连接的服务器还不支持工作区，稍后刷新再试。",
  },
  col: { name: "名称", status: "状态" },
  editNamed: "编辑 {{name}}",
  budgetNamed: "预算：{{name}}",
  form: {
    name: "名称",
    nameMissing: "填写名称。",
    disable: "停用这个工作区",
    disableHint: "停用后，它里面所有 key 的下一次调用都会被拒绝，也不能再在里面建 key；重新启用即恢复。",
    defaultHint: "默认工作区不能停用：不指定工作区建的 key 都放在这里。",
    save: "保存",
    create: "新建",
  },
  edit: "编辑工作区",
  budget: {
    title: "预算",
    none: "没有预算：这个工作区的 key 只受各自的花费上限和余额限制。",
    used: "已用 {{used}}，预算 {{limit}}",
    left: "剩余 {{left}}",
    resets: "{{time}} 重置",
    never: "不重置",
    usedHint: "已用包括正在进行的调用预留的金额；已经开始的调用超出预算也照常完成和收费。",
    limit: "预算（{{currency}}）",
    limitHint: "工作区里所有 key 共用。留空即取消预算；0 拒绝所有要花钱的调用。",
    limitInvalid: "填写金额，例如 12.50，或留空。",
    unavailable: "当前连接的服务器还不支持工作区预算，稍后再试。",
    save: "保存预算",
  },
  key: {
    field: "工作区",
    hint: "key 建好后不能换工作区。",
    filter: "工作区",
    any: "全部工作区",
    in: "工作区：{{name}}",
    lost: "“{{name}}”已停用，不能在里面建 key。请另选一个工作区。",
    moved: "这个 key 建在了“{{actual}}”，不是你选的“{{asked}}”：处理请求的服务器还不支持工作区。删除它，稍后再建。",
  },
  noPermission: {
    title: "没有查看工作区的权限",
    body: "向工作区管理员申请 api_keys.view 权限。",
  },
  settings: {
    title: "工作区",
    body: "需要把生产和测试的 key 分开、各设预算时，新建一个工作区。有了第二个工作区，菜单里才会出现它。",
    link: "管理工作区",
  },
};

export const en: Messages<typeof zh> = {
  title: "Workspaces",
  description: "Keep keys apart, such as production and testing: each workspace has its own keys and budget.",
  create: "New workspace",
  default: "Default",
  keys: "View keys",
  keysNamed: "View keys: {{name}}",
  unavailable: {
    title: "Workspaces unavailable",
    body: "The server you reached doesn’t support workspaces yet. Reload in a while.",
  },
  col: { name: "Name", status: "Status" },
  editNamed: "Edit {{name}}",
  budgetNamed: "Budget: {{name}}",
  form: {
    name: "Name",
    nameMissing: "Enter a name.",
    disable: "Disable this workspace",
    disableHint:
      "Every key in it is refused from its next call, and no key can be made in it. Enabling it brings them back.",
    defaultHint: "The default workspace can’t be disabled: keys made without a workspace go there.",
    save: "Save",
    create: "Create",
  },
  edit: "Edit workspace",
  budget: {
    title: "Budget",
    none: "No budget: the workspace’s keys are held only by their own spend limits and the balance.",
    used: "{{used}} used of {{limit}}",
    left: "{{left}} left",
    resets: "Resets {{time}}",
    never: "Never resets",
    usedHint: "Used includes what calls in progress hold. A call already started finishes and is charged even past it.",
    limit: "Budget ({{currency}})",
    limitHint: "Shared by every key in the workspace. Empty removes the budget; 0 refuses every call that costs money.",
    limitInvalid: "Enter an amount such as 12.50, or leave it empty.",
    unavailable: "The server you reached doesn’t support workspace budgets yet. Try again in a while.",
    save: "Save budget",
  },
  key: {
    field: "Workspace",
    hint: "A key can’t move to another workspace once made.",
    filter: "Workspace",
    any: "Any workspace",
    in: "Workspace: {{name}}",
    lost: "“{{name}}” was disabled, so no key can be made in it. Choose another workspace.",
    moved:
      "This key was made in “{{actual}}”, not in “{{asked}}”: the server that took the request doesn’t support workspaces yet. Delete it and make it again later.",
  },
  noPermission: {
    title: "No access to workspaces",
    body: "Ask an admin of the workspace for the api_keys.view permission.",
  },
  settings: {
    title: "Workspaces",
    body: "To keep production and test keys apart, each with a budget, make a workspace. Workspaces appear in the menu once there is a second one.",
    link: "Manage workspaces",
  },
};
