# structs-grass
The Guild Rapid Alert System Stream (structs-grass) provides a constant real-time stream of all updates to the [Structs](https://playstructs.com) game state.

GRASS, built on [NATS](https://nats.io/), can be consumed by any supported library or client. 

- GRASS messages have two components, a `subject` and `payload`.
- All GRASS message payloads are sent in `json` format.
- A `category` attribute within the payload signals what data can be expected.
- a `stub` attribute with a boolean value of `true` signals that the payload was too large to include all details. An API call may be needed to retrieve the full information.

Most players within the Structs ecosystem will not need to operate the code in this repository.

## Structs
In the distant future the species of the galaxy are embroiled in a race for Alpha Matter, the rare and dangerous substance that fuels galactic civilization. Players take command of Structs, a race of sentient machines, and must forge alliances, conquer enemies and expand their influence to control Alpha Matter and the fate of the galaxy.

[Structs](https://playstructs.com) is a decentralized game in the Cosmos ecosystem, operated and governed by our community of players--ensuring Structs remains online as long as there are players to play it.

## Get started
```bash
go build .
grass -channel grass -dbhost postgres://structs_indexer@localhost:5432/structs -nathost nats://127.0.0.1:4222 
```

## Relay behavior

Run one GRASS relay per PostgreSQL channel and NATS destination. PostgreSQL
delivers each notification to every listener, so multiple replicas publish
duplicates. GRASS forwards the original JSON bytes on the payload's subject.

The relay reads one dedicated LISTEN connection. Up to 1,024 messages can wait
in its queue. When the queue fills, the reader waits for space. A single
publisher sends queued updates in order, up to 128 per batch, and waits for a
NATS flush round trip. A lone notification is sent immediately. On a broken
connection the relay retains the current batch in memory and retries every
two seconds until delivery resumes. An interrupted flush can cause a batch
to be repeated. Invalid notifications and payloads exceeding the broker's
limit are logged and skipped.

PostgreSQL LISTEN and Core NATS provide live updates. A relay restart can lose
in-memory work, PostgreSQL notifications issued while LISTEN is disconnected
are missed, and clients offline at publication time must refresh from the API.
GRASS establishes a new LISTEN subscription automatically after a database
disconnect. During a prolonged broker outage, backpressure can reach the
PostgreSQL connection.

## Learn more

- [Structs](https://playstructs.com)
- [Project Wiki](https://watt.wiki)
- [@PlayStructs Twitter](https://twitter.com/playstructs)


## License

Most of this was written by Jon Brown in this [blog post](https://brojonat.com/posts/go-postgres-listen-notify/), so it doesn't feel right to plaster a license on.

## Influence: complete state updates and replay

Influence uses a separate durable outbox from structs-pg
`influence-grass-20261111`. Build this package with `go build .`, not `go build grass.go`. `-influence=true` (default) runs the ordered outbox drain alongside the
ordinary notification relay; `-influence=false` disables that worker.
`-influence-destination=default` names a persistent SQL delivery checkpoint. Keep
that name stable across restarts and unique for independent NATS destinations.
The paired sync-state writer must call `influence_grass_flush` after each block.

This path has **no stubs and no API-refetch messages**. PostgreSQL NOTIFY is only
a wakeup. The relay publishes committed payloads and advances its checkpoint only
after a successful NATS flush. It polls after missed wakeups and drains on startup.
A database session advisory lock permits one publisher per destination. Retries
can duplicate frames; clients deduplicate by exact frame ID. All outbox frames
remain available for client replay after the delivery cursor advances.

Each live packet has this envelope:

```json
{
  "contract":"structs-influence-grass-v1",
  "subject":"structs.influence.structstestnet-111.1.scores",
  "category":"influence_part",
  "frame_id":"123",
  "part_index":0,
  "part_count":2,
  "sha256":"<digest of complete UTF-8 JSON bytes>",
  "encoding":"base64",
  "payload":"<base64 chunk>"
}
```

Raw chunks are always 4,096 bytes, independent of connection and broker, so live
and replay parts are identical. NATS `max_payload` must be at least 8,192 bytes.
Reassemble in index order, verify the digest, decode JSON, then atomically apply
the complete frame. Never apply a partial frame. Reconstructed envelopes contain
current values, block/generation identity, per-scope `previous_frame_id`, and
history deltas or bounded replacement segments. Refer to
`structs-pg/docs/influence-grass-20261111.md` for the categories and semantics.

To recover an offline client using Core NATS:

1. Subscribe to the exact live scope first and buffer its live frames.
2. Subscribe to a fresh `_INBOX.<random>` reply subject. Publish a JSON request
   to the live subject with `replay.` inserted after `structs.influence.`:
   `structs.influence.replay.<chain>.<contest>.<scope>`.
3. Send `{"after_frame_id":"123","part_index":0}`. Use `"0"` for initial
   bootstrap: retained replacement state/history means no HTTP bootstrap is
   required either. All IDs are decimal strings, never JavaScript numbers.
   Replay starts at the latest replacement within the pinned range when one
   supersedes the saved cursor, avoiding obsolete generations.
4. Receive up to 32 `influence_part` packets and a final
   `influence_replay_end`. The final packet contains `subject`, `after_frame_id`,
   `through_frame_id`, `part_index`, and `done` (or `error`). Send those three
   cursor fields in the next request. `through_frame_id` pins the upper bound;
   `part_index` resumes a partially delivered frame without skipping its prefix.
5. Continue until `done:true`, then apply queued live frames in order, ignoring
   duplicates. On timeout, retry from the last fully applied scope frame.

Replay queries only the requested scope, has a five-second DB deadline, caps
request bytes and concurrent queued requests, and never returns an HTTP URL.
History replacement uses at most 512 rows per logical frame. Current rosters can
span many parts/pages. A slow or missing broker does not discard influence
frames. Retention is currently explicit: **do not prune the outbox**, because
client replay depends on it. Core NATS flush is still not a client acknowledgment.

NATS permissions must permit scoped live subscriptions, their corresponding
replay requests, reply inbox subscriptions, and the relay's reply publications.
These subjects contain public gameplay scores, not private account data. The
SQL connection uses the existing `structs_indexer` grants. Existing non-influence
messages retain the live-only semantics described above.
