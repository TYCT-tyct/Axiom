import first from "@axiom/boards/Site/regions/FirstScreen.zh";
import headline from "@axiom/boards/Site/regions/Headline.zh";
import wide from "@axiom/boards/Site/regions/Shell.zh";
import narrow from "@axiom/boards/SitePhone/regions/Shell.zh";

// The website's messages in Chinese, the source every other language follows.
export default {
  home: "首页",
  navigation: "主导航",
  models: "模型与价格",
  signIn: "登录",
  // the home page's words, by the keys the board's regions give them (packages/boards/regions.ts)
  board: { ...wide, ...narrow, ...first, ...headline },
  // the language as its own switch names it
  language: "简体中文",
  theme: {
    toDark: "切换到深色",
    toLight: "切换到浅色",
  },
  footer: "© Axiom · 本站中文使用了 MiSans 字体",
};
