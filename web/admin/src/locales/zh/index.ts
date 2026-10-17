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
  },
  users,
  suppliers,
  mappings,
  models,
  pricing,
  settings,
};
