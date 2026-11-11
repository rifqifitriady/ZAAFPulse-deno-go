// One run of the gateway process (P3 D1, D3, D4): start it with pipes, run the handshake, push the first config,
// secrets and budget snapshot, wait for `ready`, hand its events to onEvent and acknowledge them in batches, forward
// health, and stop it with `shutdown` (killed when it does not exit in graceMs + killAfterMs). Restarts are the
// supervisor's (lib/ipc/supervisor.js). stderr lines go to log with a "gw: " prefix.
// A protocol error, a hello that offers no common protocol, or an onEvent that throws (the event could not be
// stored, so it must not be acknowledged) ends the session: the process is killed and `ready` rejects if pending.
const { spawn } = require('child_process');
const { createChannel } = require('./channel');
const { createLineReader } = require('./framing');
const { createAcker } = require('./acker');
const { PROTOCOL, EVENTS } = require('./schema');

const EVENT = new Set(EVENTS);

/**
 * @param {{ bin: string, args?: string[], env?: Record<string, string | undefined>, cpVersion: string,
 *   initial: { config: object, secrets: object, snapshot: object },
 *   onEvent?: (msg: any, stream: string) => void, onHealth?: (body: any) => void, log?: (line: string) => void,
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
  onHealth = () => {},
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
  child.once('exit', (code, signal) => finish(code, signal));
  child.once('error', (err) => {
    log(`gw: the gateway could not be started: ${err.message}`);
    finish(null, null);
  });

  const stderr = createLineReader({
    maxBytes: 64 * 1024,
    onLine: (line) => log(`gw: ${line}`),
    onError: () => log('gw: a log line was longer than 64 KiB; the rest of its log is dropped'),
  });
  child.stderr.on('data', (c) => stderr.push(c));

  const channel = createChannel({ input: child.stdout, output: child.stdin, strict, log });
  const send = (type, body, extra = {}) => channel.send({ v: 1, type, seq: ++seq, ts: now(), ...extra, body });
  const acker = createAcker({ send: (body) => send('ack', body), everyMs: ackEveryMs, max: ackMax });
  const helloTimer = setTimeout(() => fail(new Error(`no hello within ${helloMs} ms`)), helloMs);

  function kill() {
    if (!exitedAs) child.kill('SIGKILL');
  }
  let failed = false;
  function fail(err) {
    if (failed) return;
    failed = true; // what the gateway sent after this is dropped: not stored, not acknowledged, sent again later
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
      send('hello.ok', { protocol: PROTOCOL, cpVersion }, reply);
      send('config.push', initial.config);
      send('secrets.handoff', initial.secrets);
      send('budget.snapshot', initial.snapshot);
    } else if (!stream) fail(new Error(`protocol error: ${msg.type} before hello`));
    else if (EVENT.has(msg.type)) {
      try {
        onEvent(msg, stream);
      } catch (err) {
        fail(new Error(`an event could not be stored, so it is not acknowledged: ${err.message}`));
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
