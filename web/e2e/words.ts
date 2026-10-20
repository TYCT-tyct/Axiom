import en from "../admin/src/locales/en/index.ts";
import zh from "../admin/src/locales/zh/index.ts";
import sharedEn from "../packages/ui/src/locales/en.ts";
import sharedZh from "../packages/ui/src/locales/zh.ts";

// The words of the admin's pages, found by their key in the translation files instead of written out a second time, so a
// test cannot drift from the page. A word is a pattern that matches the Chinese and the English text, with what a page
// fills in (an {{interpolation}}) matching anything.

type Tree = { [key: string]: string | Tree };

function lookup(tree: Tree, path: string) {
  let node: string | Tree | undefined = tree;
  for (const part of path.split(".")) {
    node = typeof node === "object" ? node[part] : undefined;
  }
  return typeof node === "string" ? node : undefined;
}

// The message at path: the admin's own, or the one every app shares (the states of a view of data, for one).
function message(path: string) {
  const text = lookup(zh as Tree, path) ?? lookup(sharedZh as Tree, path);
  const other = lookup(en as Tree, path) ?? lookup(sharedEn as Tree, path);
  if (text === undefined || other === undefined) {
    throw new Error(`no message at ${path}`);
  }
  return { text, other };
}

const quote = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const fill = (text: string) => quote(text).replace(/\\\{\\\{\w+\\\}\\\}/g, ".*");

export const words = (path: string) => {
  const { text, other } = message(path);
  return new RegExp(`^(${fill(text)}|${fill(other)})$`);
};

// The Chinese text of a message, for the checks that read the page in Chinese.
export const zhText = (path: string) => message(path).text;
