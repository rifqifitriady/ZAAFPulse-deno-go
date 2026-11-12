// Batched acknowledgements (P3 D4): the control plane acknowledges an event once it is stored, at most every
// `everyMs` ms or as soon as `max` events wait, with one `ack { stream, upTo }` for the highest seq stored.
/**
 * @param {{ send: (body: { stream: string, upTo: number }) => void, everyMs?: number, max?: number,
 *   setTimer?: typeof setTimeout, clearTimer?: typeof clearTimeout }} opts
 */
function createAcker({ send, everyMs = 100, max = 256, setTimer = setTimeout, clearTimer = clearTimeout }) {
  let stream = null;
  let upTo = 0;
  let sent = 0;
  let waiting = 0;
  let timer = null;
  function flush() {
    if (timer) clearTimer(timer);
    timer = null;
    waiting = 0;
    if (stream && upTo > sent) {
      try {
        send({ stream, upTo });
        sent = upTo;
      } catch {
        /* not acknowledged (the mark file could not be written): the next flush tries again */
      }
    }
  }
  return {
    /** An event of `s` with sequence `seq` is stored (or was a duplicate). */
    note(s, seq) {
      if (s !== stream) [stream, upTo, sent] = [s, 0, 0];
      if (seq > upTo) upTo = seq;
      if (++waiting >= max) flush();
      else if (!timer) {
        timer = setTimer(flush, everyMs);
        if (timer && timer.unref) timer.unref();
      }
    },
    flush,
    /** Drops a pending timer without sending (the session is over). */
    stop() {
      if (timer) clearTimer(timer);
      timer = null;
    },
  };
}

module.exports = { createAcker };
