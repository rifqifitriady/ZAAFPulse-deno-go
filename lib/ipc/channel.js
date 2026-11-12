// The transport of the IPC (P3 D1): messages over a pair of streams, one per line, behind send(msg), onMessage(fn),
// onClose(fn) and close(). The stdio pipes of the gateway process are the only transport of v1; a network transport
// can implement the same four functions later without changing a message.
// Incoming lines are checked against ipc/v1: leniently by default (unknown fields ignored, D2), strictly in tests. An
// unknown type is logged once per type and dropped. A line that is too long, not JSON or not a valid message closes
// the channel with { reason: 'protocol', error }; the end of the input closes it with { reason: 'eof' }.
const { createLineReader, MAX_LINE } = require('./framing');
const schema = require('./schema');

/**
 * @param {{ input: NodeJS.ReadableStream, output: NodeJS.WritableStream, strict?: boolean, maxBytes?: number,
 *   log?: (line: string) => void }} opts
 */
function createChannel({ input, output, strict = false, maxBytes = MAX_LINE, log = () => {} }) {
  const listeners = [];
  const closers = [];
  const unknown = new Set();
  let closed = false;

  function onLine(line) {
    if (closed) return;
    let msg;
    try {
      msg = JSON.parse(line);
    } catch {
      return close({ reason: 'protocol', error: 'a line is not JSON' });
    }
    const r = schema.check(msg, { lenient: !strict });
    if (r.errors.length) return close({ reason: 'protocol', error: `${msg && msg.type}: ${r.errors[0]}` });
    if (r.unknownType) {
      if (!unknown.has(msg.type)) {
        unknown.add(msg.type);
        log(`ipc: unknown message type "${msg.type}" dropped`);
      }
      return;
    }
    for (const fn of listeners) fn(msg);
  }
  const reader = createLineReader({
    maxBytes,
    onLine,
    onError: (err) => close({ reason: 'protocol', error: err.message }),
  });
  const onData = (chunk) => reader.push(chunk);
  const onEnd = () => {
    reader.end();
    close({ reason: 'eof' });
  };
  const onError = (err) => close({ reason: 'error', error: err.message });
  input.on('data', onData);
  input.on('end', onEnd);
  input.on('error', onError);
  // A write to a pipe whose reader died must not throw out of the process: the channel just closes.
  output.on('error', onError);

  function close(info = { reason: 'closed' }) {
    if (closed) return;
    closed = true;
    input.off('data', onData);
    input.off('end', onEnd);
    for (const fn of closers) fn(info);
  }

  return {
    /** Writes one message; false when the channel is closed. strict: a message that is not valid v1 throws. */
    send(msg) {
      if (closed) return false;
      if (strict) {
        const r = schema.check(msg);
        if (r.errors.length || r.unknownType)
          throw new Error(`ipc: refusing to send an invalid ${msg.type}: ${r.errors[0] || 'unknown type'}`);
      }
      output.write(`${JSON.stringify(msg)}\n`);
      return true;
    },
    onMessage: (fn) => listeners.push(fn),
    onClose: (fn) => closers.push(fn),
    close,
    get closed() {
      return closed;
    },
  };
}

module.exports = { createChannel };
