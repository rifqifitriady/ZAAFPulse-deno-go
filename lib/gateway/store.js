// @ts-check
// Small JSON stores for gateway data that must survive restarts (.data/, git-ignored):
// API keys, agents, sessions and memory. Writes are atomic and debounced.
const fs = require('fs');
const path = require('path');

// log: where a debounced save that failed is reported (once per store); it never throws from its timer.
/**
 * @param {string} file
 * @param {any} fallback
 * @param {{ log?: (line: string) => void }} [opts]
 */
function jsonStore(file, fallback, { log = console.error } = {}) {
  let value;
  let timer = null;
  let warned = false;
  try {
    value = JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch {
    value = typeof fallback === 'function' ? fallback() : structuredClone(fallback);
  }
  const flush = () => {
    timer = null;
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(`${file}.tmp`, JSON.stringify(value, null, 2), { mode: 0o600 });
    fs.renameSync(`${file}.tmp`, file);
  };
  return {
    get: () => value,
    set(next) {
      value = next;
      this.save(true);
    },
    // now=true writes immediately (keys, agents); otherwise within a second (sessions).
    save(now = false) {
      if (now) {
        clearTimeout(timer);
        flush();
      } else if (!timer)
        timer = setTimeout(() => {
          try {
            flush();
          } catch (err) {
            // A save from a timer has no caller to tell (P4b review m7); the next save tries again.
            if (!warned) log(`store: ${path.basename(file)} could not be saved: ${err.code || err.message}`);
            warned = true;
          }
        }, 1000);
    },
    flush() {
      if (timer) {
        clearTimeout(timer);
        flush();
      }
    },
  };
}

// Append-only JSONL with a size cap (keeps one rotated file). append never throws for the request path; with
// { strict: true } (the IPC ingest, lib/ipc/ingest.js) a failed append throws, so the event is not acknowledged.
function jsonlLog(file, maxBytes = 10 * 1024 * 1024) {
  return {
    /** @param {unknown} entry @param {{ strict?: boolean } | null} [opts] */
    append(entry, opts) {
      try {
        fs.mkdirSync(path.dirname(file), { recursive: true });
        try {
          if (fs.statSync(file).size > maxBytes) fs.renameSync(file, `${file}.1`);
        } catch {
          /* new file */
        }
        fs.appendFileSync(file, JSON.stringify(entry) + '\n', { mode: 0o600 });
      } catch (err) {
        if (opts && opts.strict) throw err;
        /* logging must never break a request */
      }
    },
    read() {
      const out = [];
      for (const f of [`${file}.1`, file]) {
        let text = '';
        try {
          text = fs.readFileSync(f, 'utf8');
        } catch {
          continue;
        }
        for (const line of text.split('\n')) {
          if (!line) continue;
          try {
            out.push(JSON.parse(line));
          } catch {
            /* partial line */
          }
        }
      }
      return out;
    },
  };
}

module.exports = { jsonStore, jsonlLog };
