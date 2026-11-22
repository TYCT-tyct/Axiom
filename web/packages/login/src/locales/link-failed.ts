// A page a mailed link opens (reset, verification) whose code did not load: the link's token is gone with a reload.
// Kept in its own tiny module so a host's route error component can show it offline without pulling every login
// message into the app's first screen.
export const linkFailed = {
  zh: "这个页面没能载入。如果是从邮件里的链接打开的，请回到邮件重新打开那个链接；否则刷新页面再试一次。",
  en: "This page didn’t load. If you opened it from an email’s link, go back to the email and open the link again; otherwise reload the page.",
} as const;
