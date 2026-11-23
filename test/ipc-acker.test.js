// The control plane's batched acknowledgements (lib/ipc/acker.js). P4a T2-f: an ack whose send fails (the mark file
// could not be written, beforeAck threw) is tried again after everyMs, also when no other event arrives; before, an
// idle acker never retried and the gateway's outbox kept the events until the next one came.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createAcker } = require('../lib/ipc/acker');

function fakeTimers() {
  const due = [];
  return {
    due,
    setTimer: (fn) => (due.push(fn), due.length),
    clearTimer: () => {},
    run: () => due.shift()(),
  };
}

test('a failed ack is tried again on the next tick without a new event (P4a T2-f)', () => {
  const t = fakeTimers();
  let fails = 1;
  const sent = [];
  const acker = createAcker({
    send: (b) => {
      if (fails-- > 0) throw new Error('the mark file could not be written');
      sent.push(b);
    },
    setTimer: t.setTimer,
    clearTimer: t.clearTimer,
  });
  acker.note('s', 3);
  t.run(); // the first flush fails
  assert.deepEqual(sent, []);
  assert.equal(t.due.length, 1, 'a retry is scheduled');
  t.run();
  assert.deepEqual(sent, [{ stream: 's', upTo: 3 }]);
  assert.equal(t.due.length, 0, 'nothing more once it is acknowledged');
});

test('a retry keeps failing quietly until it succeeds, and stop() ends it', () => {
  const t = fakeTimers();
  let ok = false;
  const sent = [];
  const acker = createAcker({
    send: (b) => {
      if (!ok) throw new Error('down');
      sent.push(b);
    },
    setTimer: t.setTimer,
    clearTimer: t.clearTimer,
  });
  acker.note('s', 1);
  t.run();
  t.run();
  assert.equal(t.due.length, 1);
  acker.note('s', 2); // a new event while a retry waits: no second timer
  assert.equal(t.due.length, 1);
  ok = true;
  t.run();
  assert.deepEqual(sent, [{ stream: 's', upTo: 2 }]);
  acker.note('s', 3);
  acker.stop();
  assert.equal(sent.length, 1);
});
