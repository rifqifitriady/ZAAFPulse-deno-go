// Framing of the IPC (P3 D2): UTF-8 JSON, one message per line. A reader joins the chunks of a stream into lines (a
// line may arrive in pieces, and a character may be cut between two chunks), removes one trailing \r, skips empty
// lines, and refuses a line longer than 8 MiB: onError is called once and the reader stops.
const MAX_LINE = 8 * 1024 * 1024;

/**
 * @param {{ onLine: (line: string) => void, onError: (err: Error) => void, maxBytes?: number }} opts
 * @returns {{ push: (chunk: Uint8Array | string) => void, end: () => void }}
 */
function createLineReader({ onLine, onError, maxBytes = MAX_LINE }) {
  let parts = [];
  let size = 0;
  let dead = false;
  const fail = (message) => {
    dead = true;
    parts = [];
    size = 0;
    onError(new Error(message));
  };
  function push(chunk) {
    if (dead) return;
    const buf = typeof chunk === 'string' ? Buffer.from(chunk, 'utf8') : Buffer.from(chunk);
    let start = 0;
    for (;;) {
      const nl = buf.indexOf(10, start);
      if (nl < 0) break;
      const piece = buf.subarray(start, nl);
      if (size + piece.length > maxBytes) return fail(`a message line is longer than ${maxBytes} bytes`);
      let line = size ? Buffer.concat([...parts, piece]) : piece;
      parts = [];
      size = 0;
      start = nl + 1;
      if (line.length && line[line.length - 1] === 13) line = line.subarray(0, -1);
      if (line.length) onLine(line.toString('utf8'));
      if (dead) return;
    }
    const rest = buf.subarray(start);
    if (size + rest.length > maxBytes) return fail(`a message line is longer than ${maxBytes} bytes`);
    if (rest.length) {
      parts.push(rest);
      size += rest.length;
    }
  }
  function end() {
    if (!dead && size) fail('the stream ended inside a message line');
  }
  return { push, end };
}

module.exports = { createLineReader, MAX_LINE };
