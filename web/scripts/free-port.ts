import { createServer } from "node:net";

// What trying to listen on an address says about it.
export type Listening = "free" | "taken" | "absent";

// classify reads the error of a failed listen. A port in use, or one the system keeps back (Windows reserves ranges, which
// answer EACCES), cannot be used: "taken", try another. An address family the machine does not have (no IPv6) cannot be
// taken by anyone, so it says nothing about the port: "absent". Any other failure is not understood, and is thrown with
// where it happened and why, not mistaken for a free port.
export function classify(error: NodeJS.ErrnoException, host: string, port: number): Listening {
  switch (error.code) {
    case "EADDRINUSE":
    case "EACCES":
      return "taken";
    case "EADDRNOTAVAIL":
    case "EAFNOSUPPORT":
      return "absent";
    default:
      throw new Error(`cannot tell whether ${host}:${port} is free: ${error.code ?? "error"}: ${error.message}`, {
        cause: error,
      });
  }
}

function listening(port: number, host: string): Promise<Listening> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", (error: NodeJS.ErrnoException) => {
      try {
        resolve(classify(error, host, port));
      } catch (unexpected) {
        reject(unexpected);
      }
    });
    server.listen(port, host, () => server.close(() => resolve("free")));
  });
}

// isFree says whether nothing listens on the port on the addresses a preview server may take.
export async function isFree(port: number): Promise<boolean> {
  const answers = await Promise.all([listening(port, "127.0.0.1"), listening(port, "::1")]);
  return !answers.includes("taken");
}

// freeFirstPort finds a first port from which the three apps' dev ports and their preview ports (vite.shared.ts, offset
// 1000) are all free. It is random, so two runs starting together do not look at the same ports one after the other and
// both take them.
export async function freeFirstPort(): Promise<number> {
  for (let attempt = 0; attempt < 100; attempt++) {
    const first = 20_000 + Math.floor(Math.random() * 20_000);
    const wanted = [0, 1, 2].flatMap((app) => [first + app, first + app + 1000]);
    if ((await Promise.all(wanted.map(isFree))).every(Boolean)) {
      return first;
    }
  }
  throw new Error("no free ports for the preview servers");
}
