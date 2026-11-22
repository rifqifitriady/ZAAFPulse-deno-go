// Fixtures for the Go egress proxy's public mode (P5, Task 2): the real lib/net.js isPrivateAddress run over every range
// boundary it knows, IPv4-mapped IPv6, NAT64, IPv6 unique-local, link-local and multicast, and the public neighbours of
// each range, written to gw/testdata/egress/private.json as { fn, in: [ip], out: [{ ip, private }] }.
// gw/egress/private_test.go replays every entry against IsPrivate. Addresses are written in the canonical form a
// resolver gives (and Go's net.IP.String() prints), the only form the dialer ever sees; a few non-addresses show that
// anything that is not an address counts as private.
// Run: node scripts/fixtures/gen-egress.js. No network, env, subprocess or sys access; writes only under gw/testdata.
const { writeCases } = require('./lib');
const { isPrivateAddress } = require('../../lib/net');

const V4 = [
  // 0.0.0.0/8 and its neighbour
  '0.0.0.0',
  '0.255.255.255',
  '1.0.0.0',
  '1.1.1.1',
  // 10.0.0.0/8
  '9.255.255.255',
  '10.0.0.0',
  '10.0.0.1',
  '10.255.255.255',
  '11.0.0.0',
  // 100.64.0.0/10 (carrier-grade NAT)
  '100.63.255.255',
  '100.64.0.0',
  '100.100.100.100',
  '100.127.255.255',
  '100.128.0.0',
  // 127.0.0.0/8
  '126.255.255.255',
  '127.0.0.0',
  '127.0.0.1',
  '127.0.0.2',
  '127.255.255.255',
  '128.0.0.0',
  // 169.254.0.0/16 (link-local, cloud metadata)
  '169.253.255.255',
  '169.254.0.0',
  '169.254.169.254',
  '169.254.255.255',
  '169.255.0.0',
  // 172.16.0.0/12
  '172.15.255.255',
  '172.16.0.0',
  '172.20.1.1',
  '172.31.255.255',
  '172.32.0.0',
  // 192.0.0.0/16 (as lib/net.js draws it) and 192.168.0.0/16
  '191.255.255.255',
  '192.0.0.0',
  '192.0.2.1',
  '192.0.255.255',
  '192.1.0.0',
  '192.167.255.255',
  '192.168.0.0',
  '192.168.1.1',
  '192.168.255.255',
  '192.169.0.0',
  // 198.18.0.0/15 (benchmarking)
  '198.17.255.255',
  '198.18.0.0',
  '198.19.255.255',
  '198.20.0.0',
  // 224.0.0.0 and above (multicast, reserved, broadcast)
  '223.255.255.255',
  '224.0.0.0',
  '239.255.255.255',
  '240.0.0.0',
  '255.255.255.255',
  // public
  '8.8.8.8',
  '93.184.216.34',
  '203.0.113.7',
];

const V6 = [
  '::',
  '::1',
  '::2',
  // IPv4-mapped
  '::ffff:127.0.0.1',
  '::ffff:10.1.2.3',
  '::ffff:169.254.169.254',
  '::ffff:192.168.0.1',
  '::ffff:100.64.0.1',
  '::ffff:8.8.8.8',
  '::ffff:1.0.0.0',
  // NAT64 well-known prefix
  '64:ff9b::',
  '64:ff9b::808:808',
  '64:ff9b::7f00:1',
  '64:ff9a::1',
  '64:ff9c::1',
  // unique local fc00::/7
  'fbff:ffff:ffff:ffff:ffff:ffff:ffff:ffff',
  'fc00::',
  'fc00::1',
  'fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff',
  'fe00::',
  // link-local fe80::/10
  'fe7f:ffff:ffff:ffff:ffff:ffff:ffff:ffff',
  'fe80::',
  'fe80::1',
  'febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff',
  'fec0::',
  // multicast ff00::/8
  'feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff',
  'ff00::',
  'ff02::1',
  'ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff',
  // public
  '2001:4860:4860::8888',
  '2606:4700::1111',
  '2001:db8::1',
];

const NOT_ADDRESSES = ['', 'localhost', 'example.com', '1.2.3', '1.2.3.256'];

const IPS = [...V4, ...V6, ...NOT_ADDRESSES];

writeCases('egress', [
  {
    name: 'private',
    fn: 'isPrivateAddress',
    args: IPS,
    call: (ips) => ips.map((ip) => ({ ip, private: isPrivateAddress(ip) })),
  },
]);
