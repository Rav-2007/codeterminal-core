// A bounded text accumulator. Every parser writes through one so the character
// limit is enforced in a single place, and a parser can stop reading the moment
// the sink is full instead of parsing a 900-page document to discard it.

export class Sink {
  private parts: string[] = [];
  private len = 0;
  full = false;

  constructor(private readonly maxChars: number) {}

  add(line: string): void {
    if (this.full) {
      return;
    }
    const room = this.maxChars - this.len;
    if (line.length + 1 > room) {
      this.parts.push(line.slice(0, Math.max(0, room)));
      this.len = this.maxChars;
      this.full = true;
      return;
    }
    this.parts.push(line);
    this.len += line.length + 1;
  }

  // Joins with newlines, collapsing runs of blank lines so layout padding in a
  // source document does not cost tokens.
  text(): string {
    const body = this.parts.join('\n').replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
    return this.full ? body + '\n…[truncated]' : body;
  }
}
