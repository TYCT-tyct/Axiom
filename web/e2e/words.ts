import enDashboard from "../admin/src/locales/en/dashboard.ts";
import enGoals from "../admin/src/locales/en/goals.ts";
import en from "../admin/src/locales/en/index.ts";
import enMonitor from "../admin/src/locales/en/monitor.ts";
import enOps from "../admin/src/locales/en/ops.ts";
import zhDashboard from "../admin/src/locales/zh/dashboard.ts";
import zhGoals from "../admin/src/locales/zh/goals.ts";
import zh from "../admin/src/locales/zh/index.ts";
import zhMonitor from "../admin/src/locales/zh/monitor.ts";
import zhOps from "../admin/src/locales/zh/ops.ts";
import uiEn from "../packages/ui/src/locales/en.ts";
import uiZh from "../packages/ui/src/locales/zh.ts";

// The words of the admin's pages and of the shared states, found by their key in the translation files instead of
// written out a second time, so a test cannot drift from the page. A word is a pattern that matches the Chinese and the
// English text, with what a page fills in (an {{interpolation}}) matching anything.

type Tree = { [key: string]: string | Tree };

function find(tree: Tree, path: string) {
  let node: string | Tree | undefined = tree;
  for (const part of path.split(".")) {
    node = typeof node === "object" ? node[part] : undefined;
  }
  return typeof node === "string" ? node : undefined;
}

// The operations pages keep their words in namespaces of their own (locales/load-page.ts); a key names one as
// "dashboard:title".
const namespaces: Record<string, { zh: Tree; en: Tree }> = {
  dashboard: { zh: zhDashboard as Tree, en: enDashboard as Tree },
  goals: { zh: zhGoals as Tree, en: enGoals as Tree },
  monitor: { zh: zhMonitor as Tree, en: enMonitor as Tree },
  ops: { zh: zhOps as Tree, en: enOps as Tree },
};

// A key belongs to the app's own translations or to the design system's, so both are searched.
function lookup(path: string): { zh: string; en: string } {
  const [namespace, key] = path.includes(":") ? path.split(":") : [undefined, path];
  const catalogs = namespace
    ? [namespaces[namespace]]
    : [
        { zh: zh as Tree, en: en as Tree },
        { zh: uiZh as Tree, en: uiEn as Tree },
      ];
  for (const catalog of catalogs) {
    const zhText = catalog && key ? find(catalog.zh, key) : undefined;
    const enText = catalog && key ? find(catalog.en, key) : undefined;
    if (zhText !== undefined && enText !== undefined) {
      return { zh: zhText, en: enText };
    }
  }
  throw new Error(`no message at ${path}`);
}

const quote = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const fill = (text: string) => quote(text).replace(/\\\{\\\{\w+\\\}\\\}/g, ".*");

export const words = (path: string) => {
  const text = lookup(path);
  return new RegExp(`^(${fill(text.zh)}|${fill(text.en)})$`);
};

// The Chinese text of a message, for the checks that read the page in Chinese.
export const zhText = (path: string) => lookup(path).zh;
