import type { Messages } from "../i18n";

// The messages of the redesign's components (#747): overlays, write failures, toasts. They join the shared namespace
// only in an app that renders these components, through DesignProvider, so an app still on the old components (the
// admin until A1) does not load them on its first screen.
export const zh = {
  overlay: {
    create: "创建",
    sending: "正在提交",
    retry: "重试",
    done: "完成",
    save: "保存",
    saving: "正在保存",
    saved: "已保存",
    live: "改动马上生效",
    staleTitle: "这一项刚在别处被改过",
    staleBody: "已经换成最新的内容，你刚才的改动没有保存。需要的话再改一次。",
  },
  failed: {
    title: "没有提交成功",
    kept: "你填的内容都还在，可以直接再试一次。",
    reverted: "已经恢复成原来的设置，可以再改一次。",
    unconfirmed: "不确定刚才的改动有没有生效。刷新页面，看到现在的设置后再改。",
    requestId: "请求 ID",
  },
  toast: {
    undo: "撤销",
    undone: "已撤销",
    undoFailed: "撤销没有完成，现在仍是撤销之前的样子。需要的话再改回来。",
    undoUnknown: "不确定撤销有没有生效。刷新页面看看现在的状态，再决定要不要改回来。",
  },
};

export const en: Messages<typeof zh> = {
  overlay: {
    create: "Create",
    sending: "Submitting",
    retry: "Try again",
    done: "Done",
    save: "Save",
    saving: "Saving",
    saved: "Saved",
    live: "Changes apply right away",
    staleTitle: "This was just changed elsewhere",
    staleBody:
      "It now shows the latest version, and your last change wasn’t saved. Make it again if you still want it.",
  },
  failed: {
    title: "That didn’t go through",
    kept: "Everything you entered is still here, so you can try again.",
    reverted: "The setting is back to what it was, so you can change it again.",
    unconfirmed:
      "It’s unclear whether your change took effect. Reload the page to see the current setting before changing it again.",
    requestId: "Request ID",
  },
  toast: {
    undo: "Undo",
    undone: "Undone",
    undoFailed: "The undo didn’t go through, so it is still as it was before. Change it back by hand if you need to.",
    undoUnknown:
      "It’s unclear whether the undo took effect. Reload the page to see the current state before changing it back.",
  },
};
