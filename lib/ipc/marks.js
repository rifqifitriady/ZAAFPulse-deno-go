// The control plane's high-water marks per gateway event stream (P3 D4). There is no mark file: every event stored
// from the gateway carries "ipc": { stream, seq } in its own line (the usage log, the governance chain, the MCP audit
// log, the trace store), so storing an event and marking it are one append. On start the marks are recovered from the
// tails of those files: the highest seq stored per stream. Events arrive in seq order and are stored in that order, so
// every seq at or below the mark is stored. Bound: the last TAIL_BYTES of each file are read (see the P3 plan).
const fs = require('fs');
const path = require('path');

const TAIL_BYTES = 4 * 1024 * 1024;

// The complete lines in the last `bytes` of a file ([] when it cannot be read).
function tailLines(file, bytes = TAIL_BYTES) {
  let fd;
  try {
    fd = fs.openSync(file, 'r');
  } catch {
    return [];
  }
  try {
    const size = fs.fstatSync(fd).size;
    const start = Math.max(0, size - bytes);
    const buf = Buffer.alloc(size - start);
    fs.readSync(fd, buf, 0, buf.length, start);
    const lines = buf.toString('utf8').split('\n');
    if (start > 0) lines.shift(); // the first piece may be cut
    lines.pop(); // the piece after the last newline: empty, or a line still being written
    return lines;
  } finally {
    fs.closeSync(fd);
  }
}

// The newest `n` files of a folder whose names match `re` (names sort by date).
function newest(dir, re, n) {
  try {
    return fs
      .readdirSync(dir)
      .filter((f) => re.test(f))
      .sort()
      .slice(-n)
      .map((f) => path.join(dir, f));
  } catch {
    return [];
  }
}

/** Adds the marks found in `lines` to `marks` (Map stream -> highest seq). */
function marksIn(lines, marks) {
  for (const line of lines) {
    if (!line.includes('"ipc"')) continue;
    let e;
    try {
      e = JSON.parse(line);
    } catch {
      continue;
    }
    const m = e && e.ipc;
    if (m && typeof m.stream === 'string' && Number.isSafeInteger(m.seq) && m.seq > (marks.get(m.stream) || 0))
      marks.set(m.stream, m.seq);
  }
  return marks;
}

/**
 * Whether the newest trace segment of `traceDir` ends with a span marked `ipc` (stream and seq). The trace store's
 * writeSpans throws when its index scan fails after the append has completed (lib/trace/store.js, unchanged by P3):
 * the span is then on disk, and storing it again on the resend would duplicate it. Read only on that failure path.
 * @param {string} traceDir
 * @param {{ stream: string, seq: number }} ipc
 * @param {number} [bytes]
 */
function spanInNewestSegment(traceDir, ipc, bytes = TAIL_BYTES) {
  const [file] = newest(traceDir, /^\d{4}-\d{2}-\d{2}\.ndjson$/, 1);
  if (!file) return false;
  for (const line of tailLines(file, bytes).reverse()) {
    if (!line.includes('"ipc"')) continue;
    try {
      const m = JSON.parse(line).ipc;
      if (m && m.stream === ipc.stream && m.seq === ipc.seq) return true;
    } catch {
      /* not a span line */
    }
  }
  return false;
}

/**
 * The marks of every stream, from the stores' tails and, when `marksFile` is given, the mark file (P4a E4): the larger
 * of the two wins, so a mark that fell out of the tail window is still found. The Map carries `reread()`, the same
 * recovery run again (not an entry): the ingest calls it at each new session (P4f entry 4), so the marks another
 * server on the same data folder stored since are not stored a second time here.
 * @param {{ usageFile: string, governanceDir: string, mcpAuditFile: string, traceDir: string, marksFile?: string }} files
 * @param {number} [bytes]
 * @returns {Map<string, number> & { reread: () => Map<string, number> }}
 */
function recoverMarks(files, bytes = TAIL_BYTES) {
  const { usageFile, governanceDir, mcpAuditFile, traceDir, marksFile } = files;
  const marks = marksFile ? require('./markfile').readMarkFile(marksFile) : new Map();
  const list = [
    `${usageFile}.1`,
    usageFile,
    `${mcpAuditFile}.1`,
    mcpAuditFile,
    ...newest(governanceDir, /^governance-\d{4}-\d{2}\.jsonl$/, 2),
    ...newest(traceDir, /^\d{4}-\d{2}-\d{2}\.ndjson$/, 2),
  ];
  for (const f of list) marksIn(tailLines(f, bytes), marks);
  Object.defineProperty(marks, 'reread', { value: () => recoverMarks(files, bytes) });
  return /** @type {any} */ (marks);
}

module.exports = { recoverMarks, marksIn, tailLines, spanInNewestSegment, TAIL_BYTES };
