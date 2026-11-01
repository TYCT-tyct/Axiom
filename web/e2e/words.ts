import en from "../admin/src/locales/en/index.ts";
import zh from "../admin/src/locales/zh/index.ts";
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

// A key belongs to the app's own translations or to the design system's, so both are searched.
function lookup(path: string): { zh: string; en: string } {
  for (const catalog of [
    { zh: zh as Tree, en: en as Tree },
    { zh: uiZh as Tree, en: uiEn as Tree },
  ]) {
    const zhText = find(catalog.zh, path);
    const enText = find(catalog.en, path);
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
