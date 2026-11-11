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
 * The marks of every stream, from the stores' tails.
 * @param {{ usageFile: string, governanceDir: string, mcpAuditFile: string, traceDir: string }} files
 * @param {number} [bytes]
 * @returns {Map<string, number>}
 */
function recoverMarks({ usageFile, governanceDir, mcpAuditFile, traceDir }, bytes = TAIL_BYTES) {
  const marks = new Map();
  const files = [
    `${usageFile}.1`,
    usageFile,
    `${mcpAuditFile}.1`,
    mcpAuditFile,
    ...newest(governanceDir, /^governance-\d{4}-\d{2}\.jsonl$/, 2),
    ...newest(traceDir, /^\d{4}-\d{2}-\d{2}\.ndjson$/, 2),
  ];
  for (const f of files) marksIn(tailLines(f, bytes), marks);
  return marks;
}

module.exports = { recoverMarks, marksIn, tailLines, TAIL_BYTES };
