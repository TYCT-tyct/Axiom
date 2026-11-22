import type { Messages } from "@axiom/ui";

export const zh = {
  title: "更新后的条款",
  description: "{{email}}，继续使用前请阅读并同意当前版本。",
  acceptSubmit: "同意并继续",
  missing: "没有加载到当前条款，请稍后重试。",
  terms: "服务条款",
  privacy: "隐私政策",
  version: "第 {{version}} 版",
  loading: "正在加载条款…",
  error: "条款没有加载出来。",
  retry: "重试",
  accept: "我已阅读并同意上述服务条款和隐私政策。",
};

export const en: Messages<typeof zh> = {
  title: "Updated terms",
  description: "{{email}}, read and accept the current versions before continuing.",
  acceptSubmit: "Accept and continue",
  missing: "The current documents didn’t load. Try again in a moment.",
  terms: "Terms of Service",
  privacy: "Privacy Policy",
  version: "Version {{version}}",
  loading: "Loading the documents…",
  error: "The documents didn’t load.",
  retry: "Retry",
  accept: "I have read and agree to the terms of service and privacy policy above.",
};
