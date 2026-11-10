# Conditions of the 2026-11-10 runs on MBA-ZAAF (Apple M4, 10 cores, 16 GB)

Both runs: committed tree 64369e6 (`git.dirty` false), default knobs, `ulimit -n 8192`, node run first, deno run right after.

- On battery (about 68%), not plugged in.
- Not a quiet machine: Zoom was using about 50% of a core during the runs, and the load average was 3 to 5.
  The stream overhead p95 values (a difference of two p95s of 0.5 s streams) are noisy for that reason; the 50-stream
  step even differs by 30 ms between the runs. Read the non-streaming overhead, startup and RSS as the stable numbers.
- Four demo servers from an earlier spike were present and idle (0% CPU, 77 to 91 MB RSS each) on ports 10463 to 10466
  (pids 85769, 85819, 85870, 93329). They were not touched.
- Local Node is v24.16.0 (CI pins 24.21.0); Deno is 2.9.8.
- The first Deno start took 498 ms (cold cache), the other four 250 to 271 ms; p50 is of all five.
