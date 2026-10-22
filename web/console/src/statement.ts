import { consoleApi, unwrap } from "@axiom/api";

// The statement's own pieces: the file a page offers to download, and what a row's amount means.

// statementCsv is the account's statement as the server gives it: comma-separated text, newest first.
export const statementCsv = () => unwrap(consoleApi.GET("/api/console/v1/statement.csv", { parseAs: "text" }));

// The filename uses the same UTC day as the server's Content-Disposition.
export function statementName(now = new Date()) {
  return `statement-${now.toISOString().slice(0, 10)}.csv`;
}

export function statementFile(csv: string, now = new Date()) {
  return new File([csv], statementName(now), { type: "text/csv" });
}

// save hands the file to the browser's downloads: a link clicks itself on an object URL, which is let go once the click
// has been made.
export function save(file: File) {
  const url = URL.createObjectURL(file);
  try {
    const link = document.createElement("a");
    link.href = url;
    link.download = file.name;
    link.click();
  } finally {
    URL.revokeObjectURL(url);
  }
}

// raised says whether a movement added to the balance: an amount is signed, and the contract makes a charge negative.
export function raised(amount: string) {
  return BigInt(amount) > 0n;
}
