# Security

waarm64-net runs on your own computer and puts a virtual machine on your network, so a flaw in it can reach past the
browser. Please report one privately, not in a public issue.

## Reporting

Use GitHub's private vulnerability reporting: [Report a vulnerability](https://github.com/gbl08ma/waarm64-net/security/advisories/new)
(the repository's Security tab). Say what you found, which version (`waarm64-net --version`) and platform, and how to
reproduce it. You'll get an answer there; a fix ships as a new release, and the advisory is published with it.

Only the latest release is supported: fixes aren't backported (until 1.0, the README's status line).

## What counts

The helper's promises are in the README ("What you're trusting"). A way to break any of them is a vulnerability, for
example:

- a page from an origin that isn't allowed, or without the token, getting a working connection, or learning the token;
- the helper listening on anything but the loopback address it was given;
- a guest reaching this computer's own loopback services without `--host-loopback`, or any API of the helper or its
  network library;
- a guest or a page crashing the helper, or making it use memory or CPU without bound;
- anything about the traffic in its logs, or the token anywhere but its file and the pairing links it prints;
- a release's binaries that don't rebuild to its SHA256SUMS (README "Reproducible builds"), or install lines that would
  run a binary whose hash doesn't match.

## What doesn't

- The guest reaching the internet and your LAN as this computer does: that is what the helper is for (README "What the
  guest can reach").
- Two guests on one helper seeing each other: they share a switch, as on any LAN.
- The binaries being unsigned, and the warnings Gatekeeper or SmartScreen may show (README "Install").
- Flaws in gvisor-tap-vsock, gVisor or another library that the helper doesn't expose: report those upstream (the
  list is in [THIRD_PARTY.md](THIRD_PARTY.md)). If the helper does expose one, report it here too.
