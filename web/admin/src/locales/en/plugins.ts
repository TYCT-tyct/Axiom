export default {
  tabsLabel: "Plugin pages",
  tabs: { overview: "Overview", web: "Web search", file: "File parsing" },
  lede: {
    overview:
      "How the web search and file parsing plugins were used in the last 24 hours, and which requests used them.",
    web: "When a model needs to look something up, a search engine searches first and hands the results to the model. Each search is billed at its cost times the markup.",
    file: "When a request carries a PDF, a Word file or a spreadsheet, the parser turns it into text first and hands that to the model.",
  },
  what: { web: "Used when a model looks something up", "file-parser": "Used when a request carries a file" },
  requests: "Requests in the last 24 hours",
  hourly: "Requests per hour",
  noHours: "No requests in this window",
  failures: "Failed",
  failureCount: "{{count}}",
  meanTime: "Mean time",
  uses: { web: "Searches run", "file-parser": "Pages or files parsed" },
  settings: "Settings",
  recent: "Recent requests that used a plugin",
  recentLabel: "Requests that used a plugin",
  filterLabel: "Plugin",
  col: {
    plugin: "Plugin",
    how: "How it was handled",
    time: "Plugin time",
    fee: "Plugin charge",
  },
  search: "{{engine}}, {{count}} searches",
  parse: "{{engine}}, {{count}} parsed",
  noEmpty: {
    title: "No request has used a plugin yet",
    body: "Once a search engine and a parser are set up, requests that search the web or carry a file are listed here.",
  },
};
