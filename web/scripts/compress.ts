// The compression the program ships its files with, in one place: embed.ts writes these copies for the Go server to send
// to a client that accepts them (internal/platform/webui), and budget.ts measures them, so the budget counts what is sent.
import { brotliCompressSync, constants, gzipSync } from "node:zlib";

export function brotli(data: Buffer) {
  return brotliCompressSync(data, {
    params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: data.length },
  });
}

export function gzip(data: Buffer) {
  return gzipSync(data, { level: 9 });
}
