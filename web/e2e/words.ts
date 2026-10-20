import en from "../admin/src/locales/en/index.ts";
import zh from "../admin/src/locales/zh/index.ts";

// The words of the admin's pages, found by their key in the translation files instead of written out a second time, so a
// test cannot drift from the page. A word is a pattern that matches the Chinese and the English text, with what a page
// fills in (an {{interpolation}}) matching anything.

type Tree = { [key: string]: string | Tree };

function lookup(tree: Tree, path: string) {
  let node: string | Tree | undefined = tree;
  for (const part of path.split(".")) {
    node = typeof node === "object" ? node[part] : undefined;
  }
  if (typeof node !== "string") {
    throw new Error(`no message at ${path}`);
  }
  return node;
}

const quote = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const fill = (text: string) => quote(text).replace(/\\\{\\\{\w+\\\}\\\}/g, ".*");

export const words = (path: string) =>
  new RegExp(`^(${fill(lookup(zh as Tree, path))}|${fill(lookup(en as Tree, path))})$`);

// The Chinese text of a message, for the checks that read the page in Chinese.
export const zhText = (path: string) => lookup(zh as Tree, path);
