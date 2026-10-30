// The API keeps JSON numbers as they were written: an audit ratio or a route value can exceed the range JavaScript
// parses exactly. JSON.parse would round 9007199254740993 to 9007199254740992, so the fields shown as JSON are read
// from the response text and numbers that cannot round-trip stay as their original text.
class RawNumber {
  constructor(readonly raw: string) {}
}

type JSONValue = null | boolean | number | string | RawNumber | JSONValue[] | { [key: string]: JSONValue };

class Parser {
  private at = 0;

  constructor(private readonly text: string) {}

  parse(): JSONValue {
    const value = this.value();
    this.space();
    if (this.at !== this.text.length) {
      this.fail("trailing text");
    }
    return value;
  }

  private fail(what: string): never {
    throw new SyntaxError(`${what} at ${this.at}`);
  }

  private space() {
    while (this.at < this.text.length && /\s/.test(this.text[this.at] ?? "")) {
      this.at++;
    }
  }

  private string(): string {
    const start = this.at;
    if (this.text[this.at++] !== '"') {
      this.fail("a string");
    }
    while (this.at < this.text.length) {
      const c = this.text[this.at++];
      if (c === '"') {
        return JSON.parse(this.text.slice(start, this.at)) as string;
      }
      if (c === "\\") {
        this.at++;
      }
    }
    return this.fail("the end of a string");
  }

  private number(): number | RawNumber {
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(this.text.slice(this.at));
    if (!match) {
      return this.fail("a number");
    }
    const raw = match[0];
    this.at += raw.length;
    const value = Number(raw);
    // A number that JavaScript writes back exactly is safe to hold as a number. A larger integer, an exponent form or
    // a negative zero would change, so it keeps its text.
    return JSON.stringify(value) === raw ? value : new RawNumber(raw);
  }

  private value(): JSONValue {
    this.space();
    const c = this.text[this.at];
    if (c === "{") {
      return this.object();
    }
    if (c === "[") {
      return this.array();
    }
    if (c === '"') {
      return this.string();
    }
    for (const [word, literal] of [
      ["true", true],
      ["false", false],
      ["null", null],
    ] as const) {
      if (this.text.startsWith(word, this.at)) {
        this.at += word.length;
        return literal;
      }
    }
    return this.number();
  }

  private object(): Record<string, JSONValue> {
    this.at++;
    const out: Record<string, JSONValue> = {};
    this.space();
    if (this.text[this.at] === "}") {
      this.at++;
      return out;
    }
    while (true) {
      this.space();
      const key = this.string();
      this.space();
      if (this.text[this.at++] !== ":") {
        this.fail("a colon");
      }
      out[key] = this.value();
      this.space();
      const next = this.text[this.at++];
      if (next === "}") {
        return out;
      }
      if (next !== ",") {
        this.fail("a comma or the end of an object");
      }
    }
  }

  private array(): JSONValue[] {
    this.at++;
    const out: JSONValue[] = [];
    this.space();
    if (this.text[this.at] === "]") {
      this.at++;
      return out;
    }
    while (true) {
      out.push(this.value());
      this.space();
      const next = this.text[this.at++];
      if (next === "]") {
        return out;
      }
      if (next !== ",") {
        this.fail("a comma or the end of an array");
      }
    }
  }
}

export function parseJSONLossless(text: string): JSONValue {
  return new Parser(text).parse();
}

// formatJSON writes a parsed value back with the original number text, indented as before.
export function formatJSON(value: unknown, level = 0): string {
  const pad = "  ".repeat(level);
  if (value instanceof RawNumber) {
    return value.raw;
  }
  if (Array.isArray(value)) {
    if (value.length === 0) {
      return "[]";
    }
    const inner = value.map((item) => `${"  ".repeat(level + 1)}${formatJSON(item, level + 1)}`);
    return `[\n${inner.join(",\n")}\n${pad}]`;
  }
  if (value !== null && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>);
    if (entries.length === 0) {
      return "{}";
    }
    const inner = entries.map(
      ([key, item]) => `${"  ".repeat(level + 1)}${JSON.stringify(key)}: ${formatJSON(item, level + 1)}`,
    );
    return `{\n${inner.join(",\n")}\n${pad}}`;
  }
  return JSON.stringify(value) ?? "null";
}
