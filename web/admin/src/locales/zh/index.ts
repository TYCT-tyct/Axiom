import mappings from "./mappings";
import models from "./models";
import pricing from "./pricing";
import settings from "./settings";
import suppliers from "./suppliers";
import users from "./users";

// The admin's messages in Chinese, the source every other language follows.
export default {
  brandName: "Axiom 管理后台",
  nav: {
    users: "用户",
    suppliers: "供应商",
    mappings: "待映射模型",
    models: "模型目录",
    pricing: "定价",
    settings: "站点设置",
  },
  signIn: { description: "管理后台只对运营人员开放，登录后还要完成两步验证。" },
  home: { noAccessTitle: "还没有可用的页面", noAccessBody: "你的账户没有任何管理权限。请联系负责人授权。" },
  filter: { apply: "筛选", reset: "清除" },
  change: {
    reason: "理由",
    reasonHint: "为什么做这件事。会写进审计日志，之后可以查到。",
    reasonMissing: "请填写理由。",
    confirmLabel: "输入“{{name}}”以确认",
    confirmMismatch: "输入的名称和要操作的对象不一致。",
    done: "已完成。",
    replayed: "这次操作之前已经完成过，下面是当时的结果，没有重复执行。",
    close: "关闭",
    unknown:
      "不确定这次改动有没有生效。内容已锁定：重发会原样发出同一个请求，服务端最多执行一次。要改内容，请先关闭，看过当前数据再重新操作。",
    resend: "原样重发",
    stale:
      "打开之后，这一项被别人改过。为了不覆盖那次改动，这次没有保存。重新载入会换成最新的内容，你在这里填写的会被丢弃。",
    reload: "重新载入",
    reused:
      "这个请求的标识已经用于另一份内容，第一次提交可能已经生效。请关闭后查看当前数据，确认之后再决定要不要重做。",
    check: "关闭并查看当前数据",
  },
  users,
  suppliers,
  mappings,
  models,
  pricing,
  settings,
};
