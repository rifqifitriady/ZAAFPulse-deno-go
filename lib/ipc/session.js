// One run of the gateway process (P3 D1, D3, D4): start it with pipes, run the handshake, push the first config,
// secrets and budget snapshot, wait for `ready`, hand its events to onEvent and acknowledge them in batches, forward
// health, and stop it with `shutdown` (killed when it does not exit in graceMs + killAfterMs). Restarts are the
// supervisor's (lib/ipc/supervisor.js). stderr lines go to log with a "gw: " prefix.
// onHello gets the hello body before hello.ok is sent (the ingest raises the stream's mark to outboxFrom - 1 there).
// A protocol error, a hello that offers no common protocol, or an onEvent or onHello that throws (the event could not
// be stored, so it must not be acknowledged) ends the session: the process is killed and `ready` rejects if pending.
// `failure` then says why ({ kind: 'hello' | 'protocol' | 'store', message }), so the supervisor can tell a store
// failure from a crash; it stays null when the gateway exits on its own. A hello this side refuses sets failure
// { kind: 'refused' } without a kill (the gateway exits 2) and arms no hello timer (P4a E8).
// `fatal` is the last stderr line that starts with "fatal: " (the gateway's reason before exit codes 1, 2, 4 and 5).
// `exited` resolves on the child's 'close' (stderr read to the end, so `fatal` is known), or 2 s after 'exit' when a
// pipe stays open. beforeAck(body) runs before each ack is sent (the mark file, P4a E4); when it throws, that ack is
// not sent and the next one covers it.
const { spawn } = require('child_process');
const { createChannel } = require('./channel');
const { createLineReader } = require('./framing');
const { createAcker } = require('./acker');
const { PROTOCOL, EVENTS } = require('./schema');

const EVENT = new Set(EVENTS);

/**
 * @param {{ bin: string, args?: string[], env?: Record<string, string | undefined>, cpVersion: string,
 *   initial: { config: object, secrets: object, snapshot: object },
 *   onEvent?: (msg: any, stream: string) => void, onHello?: (body: any) => void, onHealth?: (body: any) => void,
 *   log?: (line: string) => void,
 *   now?: () => number, helloMs?: number, strict?: boolean, ackEveryMs?: number, ackMax?: number,
 *   killAfterMs?: number, spawnImpl?: typeof spawn }} opts
 */
function startSession({
  bin,
  args = [],
  env = process.env,
  cpVersion,
  initial,
  onEvent = () => {},
  onHello = () => {},
  onHealth = () => {},
  beforeAck = () => {},
  log = console.error,
  now = Date.now,
  helloMs = 5000,
  strict = false,
  ackEveryMs = 100,
  ackMax = 256,
  killAfterMs = 2000,
  spawnImpl = spawn,
}) {
  const child = spawnImpl(bin, args, { stdio: ['pipe', 'pipe', 'pipe'], env, windowsHide: true });
  let seq = 0;
  let stream = null;
  let exitedAs = null;
  let settle;
  const ready = new Promise((resolve, reject) => (settle = { resolve, reject }));
  ready.catch(() => {}); // a caller that never awaits ready must not see an unhandled rejection
  let resolveExit;
  const exited = new Promise((resolve) => (resolveExit = resolve));
  const finish = (code, signal) => {
    if (exitedAs) return;
    exitedAs = { code, signal };
    clearTimeout(helloTimer);
    acker.stop();
    settle.reject(new Error(`the gateway exited (code ${code}, signal ${signal}) before it was ready`));
    resolveExit(exitedAs);
  };
  let closeTimer = null;
  child.once('exit', (code, signal) => {
    closeTimer = setTimeout(() => finish(code, signal), 2000);
  });
  child.once('close', (code, signal) => {
    clearTimeout(closeTimer);
    finish(code, signal);
  });
  child.once('error', (err) => {
    log(`gw: the gateway could not be started: ${err.message}`);
    finish(null, null);
  });

  let fatal = null;
  const stderr = createLineReader({
    maxBytes: 64 * 1024,
    onLine: (line) => {
      if (line.startsWith('fatal: ')) fatal = line.slice('fatal: '.length);
      log(`gw: ${line}`);
    },
    onError: () => log('gw: a log line was longer than 64 KiB; the rest of its log is dropped'),
  });
  child.stderr.on('data', (c) => stderr.push(c));

  const channel = createChannel({ input: child.stdout, output: child.stdin, strict, log });
  const send = (type, body, extra = {}) => channel.send({ v: 1, type, seq: ++seq, ts: now(), ...extra, body });
  let ackFailLogged = false;
  const acker = createAcker({
    send: (body) => {
      try {
        beforeAck(body);
      } catch (err) {
        if (!ackFailLogged) log(`gw: the IPC mark file could not be written, so the ack waits: ${err.message}`);
        ackFailLogged = true;
        throw err;
      }
      send('ack', body);
    },
    everyMs: ackEveryMs,
    max: ackMax,
  });
  const helloTimer = setTimeout(() => fail(new Error(`no hello within ${helloMs} ms`), 'hello'), helloMs);

  function kill() {
    if (!exitedAs) child.kill('SIGKILL');
  }
  let failed = false;
  let failure = null;
  function fail(err, kind = 'protocol') {
    if (failed) return;
    failed = true; // what the gateway sent after this is dropped: not stored, not acknowledged, sent again later
    failure = { kind, message: err.message };
    acker.stop();
    log(`gw: ${err.message}`);
    settle.reject(err);
    kill();
  }

  channel.onClose((info) => {
    if (info.reason === 'protocol') fail(new Error(`protocol error: ${info.error}`));
  });
  channel.onMessage((msg) => {
    if (failed) return;
    if (msg.type === 'hello') {
      const reply = msg.id ? { re: msg.id } : {};
      if (!msg.body.protocol.includes(PROTOCOL)) {
        clearTimeout(helloTimer); // the gateway exits 2 on its own: no kill after helloMs (T3-3)
        failure = { kind: 'refused', message: `the gateway offers protocols ${JSON.stringify(msg.body.protocol)}` };
        send(
          'hello.refused',
          {
            reason: `this control plane speaks protocol ${PROTOCOL}, the gateway offers ${JSON.stringify(msg.body.protocol)}`,
          },
          reply,
        );
        settle.reject(new Error('protocol mismatch: the gateway was refused'));
        return;
      }
      clearTimeout(helloTimer);
      stream = msg.body.stream;
      try {
        onHello(msg.body); // before hello.ok: the replay that follows must meet the raised mark
      } catch (err) {
        fail(new Error(`the hello could not be noted: ${err.message}`), 'store');
        return;
      }
      send('hello.ok', { protocol: PROTOCOL, cpVersion }, reply);
      send('config.push', initial.config);
      send('secrets.handoff', initial.secrets);
      send('budget.snapshot', initial.snapshot);
    } else if (!stream) fail(new Error(`protocol error: ${msg.type} before hello`));
    else if (EVENT.has(msg.type)) {
      try {
        onEvent(msg, stream);
      } catch (err) {
        fail(new Error(`an event could not be stored, so it is not acknowledged: ${err.message}`), 'store');
        return;
      }
      acker.note(stream, msg.seq);
    } else if (msg.type === 'ready') settle.resolve(msg.body);
    else if (msg.type === 'health') onHealth(msg.body);
    else if (msg.type === 'error')
      log(`gw: the gateway reports a protocol error: ${msg.body.code}: ${msg.body.message}`);
    else log(`gw: unexpected ${msg.type} from the gateway, dropped`);
  });

  return {
    pid: child.pid,
    get stream() {
      return stream;
    },
    /** Why the session ended the gateway, or refused it (null: neither). */
    get failure() {
      return failure;
    },
    /** The reason of the gateway's last "fatal: " log line (null: none). */
    get fatal() {
      return fatal;
    },
    ready,
    exited,
    channel,
    /** Sends a control message (config.push, secrets.handoff, budget.snapshot, ...) with the next seq. */
    send,
    flushAcks: () => acker.flush(),
    /** Acknowledges what is stored, asks the gateway to stop, and kills it when it has not exited in time. */
    async shutdown(graceMs = 5000) {
      // Asked from inside onEvent, that event's ack is noted only when onEvent returns: let it be, then flush.
      await Promise.resolve();
      if (exitedAs) return exitedAs;
      acker.flush();
      send('shutdown', { graceMs });
      const t = setTimeout(kill, graceMs + killAfterMs);
      const r = await exited;
      clearTimeout(t);
      return r;
    },
    /** Closes the gateway's stdin: it exits 0 (the control plane is gone, D1). */
    close: () => child.stdin.end(),
    kill,
  };
}

module.exports = { startSession };
