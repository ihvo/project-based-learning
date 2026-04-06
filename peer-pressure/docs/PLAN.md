# Peer Pressure — Full BEP Implementation Plan

## Status Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Implemented and tested |
| 🔨 | Partially implemented |
| ⬜ | Not started |

## Phase 1: Core Protocol (DONE)

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 3 | BitTorrent Protocol | ✅ | — |
| 10 | Extension Protocol | ✅ | — |
| 15 | UDP Tracker Protocol | ✅ | — |
| 23 | Compact Peer Lists | ✅ | — |

Packages: `bencode/`, `torrent/`, `tracker/`, `peer/`, `download/`

## Phase 2: Content Discovery (DONE)

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 5 | DHT Protocol | ✅ | — |
| 9 | Metadata Exchange (magnet links) | ✅ | — |
| 12 | Multitracker Metadata Extension | 🔨 | [bep-12.md](bep-12.md) |
| 19 | WebSeed (GetRight style) | ✅ | — |

Packages: `dht/`, `magnet/`

## Phase 3: Seeding & Identity

Upload support — turns Peer Pressure into a full participant in the swarm.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 20 | Peer ID Conventions | ⬜ | [bep-20.md](bep-20.md) |
| — | Seeding (accept, serve, CLI) | ⬜ | [seeding.md](seeding.md) |

Packages: `seed/`

## Phase 4: Peer Discovery

Expand how we find peers beyond trackers and DHT.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 11 | Peer Exchange (PEX) | ⬜ | [bep-11.md](bep-11.md) |
| 12 | Multi-tracker tier logic | ⬜ | [bep-12.md](bep-12.md) |
| 14 | Local Peer Discovery | ⬜ | [bep-14.md](bep-14.md) |

Packages: `pex/`, `discovery/`

## Phase 5: Protocol Hardening

Correctness and efficiency improvements to the peer wire protocol.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 6 | Fast Extension | ⬜ | [bep-06.md](bep-06.md) |
| 27 | Private Torrents | ⬜ | [bep-27.md](bep-27.md) |
| 40 | Canonical Peer Priority | ⬜ | [bep-40.md](bep-40.md) |
| — | Endgame mode | ⬜ | [endgame.md](endgame.md) |

## Phase 6: Transport & Performance

Modern transport and resource management.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 29 | uTorrent Transport Protocol (uTP) | ⬜ | [bep-29.md](bep-29.md) |
| — | Bandwidth throttling | ⬜ | [bandwidth.md](bandwidth.md) |

Packages: `utp/`

## Phase 7: Tracker Enhancements

Complete tracker protocol coverage.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 7 | IPv6 Tracker Extension | ⬜ | [bep-07.md](bep-07.md) |
| 24 | Tracker Returns External IP | ⬜ | [bep-24.md](bep-24.md) |
| 41 | UDP Tracker Protocol Extensions | ⬜ | [bep-41.md](bep-41.md) |
| 48 | Tracker Protocol Extension: Scrape | ⬜ | [bep-48.md](bep-48.md) |

## Phase 8: DHT Enhancements

Harden and extend the DHT implementation.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 32 | DHT Extensions for IPv6 | ⬜ | [bep-32.md](bep-32.md) |
| 42 | DHT Security Extension | ⬜ | [bep-42.md](bep-42.md) |
| 43 | Read-only DHT Nodes | ⬜ | [bep-43.md](bep-43.md) |

## Phase 9: Advanced Features

Content management and extended seeding capabilities.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 17 | HTTP Seeding (Hoffman style) | ⬜ | [bep-17.md](bep-17.md) |
| 21 | Extension for Partial Seeds | ⬜ | [bep-21.md](bep-21.md) |
| 47 | Padding Files & Extended Attributes | ⬜ | [bep-47.md](bep-47.md) |
| 53 | Magnet URI: Select Specific Files | ⬜ | [bep-53.md](bep-53.md) |
| — | Torrent creation | ⬜ | [torrent-creation.md](torrent-creation.md) |

## Phase 10: DHT Applications

Use the DHT for more than peer discovery.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 44 | Storing Arbitrary Data in DHT | ⬜ | [bep-44.md](bep-44.md) |
| 46 | Updating Torrents via DHT Mutable Items | ⬜ | [bep-46.md](bep-46.md) |
| 51 | DHT Infohash Indexing | ⬜ | [bep-51.md](bep-51.md) |

## Phase 11: BitTorrent v2

The next generation of the protocol — SHA-256, merkle trees, hybrid torrents.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 52 | BitTorrent Protocol v2 | ⬜ | [bep-52.md](bep-52.md) |

Packages: `torrentv2/`

## Phase 12: NAT Traversal

Direct connections through NAT without relay.

| BEP | Name | Status | Spec |
|-----|------|--------|------|
| 55 | Holepunch Extension | ⬜ | [bep-55.md](bep-55.md) |

## Phase 13: Quality of Life

Polish for a production-grade client.

| Feature | Status | Spec |
|---------|--------|------|
| Structured logging (slog) | ⬜ | [logging.md](logging.md) |

## Architecture Overview

```
cmd/peer-pressure/     CLI entry point
bencode/               BEP 3 codec
torrent/               .torrent parser, creator
tracker/               HTTP + UDP tracker client
peer/                  Peer wire protocol (BEP 3, 6, 10)
download/              Orchestrator, picker, pool, progress, webseed
dht/                   BEP 5 DHT (KRPC, routing, node)
magnet/                Magnet URI parser
pex/                   BEP 11 Peer Exchange         (Phase 4)
discovery/             Unified peer source manager   (Phase 4)
seed/                  Upload server                 (Phase 3)
utp/                   BEP 29 uTP transport          (Phase 6)
torrentv2/             BEP 52 v2 protocol            (Phase 11)
docs/                  Specs and plan
```
