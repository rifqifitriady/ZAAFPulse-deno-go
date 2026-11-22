// lib/net.js isPrivateAddress, the IPv6 ranges added in P5 (the same set gw/egress IsPrivate refuses, proven by
// gw/testdata/egress/private.json): IPv4-compatible ::a.b.c.d, NAT64 local-use 64:ff9b:1::/48, site-local fec0::/10,
// Teredo 2001:0::/32 and 6to4 2002::/16 with a private IPv4 address inside; any text form of an address is judged by
// its value.
const test = require('node:test');
const assert = require('node:assert/strict');
const { isPrivateAddress } = require('../lib/net');

test('isPrivateAddress: IPv6 forms that carry a private IPv4 address, and the deprecated ranges', () => {
  for (const a of [
    '::2',
    '::1.2.3.4',
    '::127.0.0.1',
    '::102:304',
    '0:0:0:0:0:0:808:808',
    '::ffff:7f00:1', // mapped 127.0.0.1 in hex groups
    '::FFFF:A9FE:A9FE', // mapped 169.254.169.254, upper case
    '64:ff9b:1::1',
    '64:ff9b:1:abcd::1.2.3.4',
    'fec0::1',
    'FEFF::1',
    '2001::1',
    '2001:0:4136:e378:8000:63bf:3fff:fdd2',
    '2002:a00:1::',
    '2002:7f00:0001::1',
    '2002:a9fe:a9fe::',
    '2002::',
    'fe80::1%en0',
  ])
    assert.equal(isPrivateAddress(a), true, a);
  for (const a of [
    '::ffff:808:808', // mapped 8.8.8.8 in hex groups
    '::ffff:8.8.8.8',
    '::1:0:0:0',
    '2001:1::1',
    '2001:db8::1',
    '2002:808:808::1',
    '2003::1',
    '2606:4700::1111',
    '1:2:3:4:5:6:1.2.3.4',
  ])
    assert.equal(isPrivateAddress(a), false, a);
});

test('isPrivateAddress: what was refused before still is', () => {
  for (const a of ['::', '::1', 'fc00::1', 'fd12::1', 'fe80::1', 'ff02::1', '64:ff9b::7f00:1', '10.0.0.1', 'localhost'])
    assert.equal(isPrivateAddress(a), true, a);
});
