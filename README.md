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

## Influence: complete updates for read-only subscribers

Influence uses a durable outbox from structs-pg `influence-grass-20261111`.
Build the whole package with `go build .`. `-influence=true` (default) runs the
ordered publisher alongside ordinary notifications; `-influence=false` disables
that worker. `-influence-destination=default` names its persistent delivery
checkpoint. Keep that name stable across restarts and unique per NATS destination.
The paired sync-state writer calls `influence_grass_flush` after each block.

Normal influence updates contain complete data, with no stubs or API-refetch
instructions. PostgreSQL NOTIFY is only a wakeup. The relay publishes committed
frames, confirms a NATS flush, then advances the destination checkpoint. It polls
after missed wakeups and drains pending rows on startup. A database advisory lock
permits one publisher per destination. Retries can duplicate frames; clients
ignore exact frame IDs they have already applied.

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

Raw chunks are always 4,096 bytes so retries use identical parts. NATS
`max_payload` must be at least 8,192 bytes. Assemble parts in index order, verify
the digest, decode JSON, and apply the complete frame. Reconstructed envelopes
contain absolute current values, block/build identity, per-scope predecessor
IDs, and history deltas or bounded replacement segments. See
`structs-pg/docs/influence-grass-20261111.md` for categories and subjects.

**Clients only subscribe. There are no NATS replay request subjects, reply
inboxes, or replay-end packets.** The relay needs publish permission for influence
subjects and does not subscribe to client requests. No JetStream setup is needed.

Clients use the influence HTTP API for initial loading and for resynchronization
after a disconnect, a detected frame gap, or an incomplete/corrupt multipart
frame. Subscribe and flush the subscription before loading the API snapshot,
buffer arriving updates, then discard frames already covered by that snapshot's
build and inclusive block height. Continue applying newer complete events without
additional API requests. If several API reads make up the snapshot, their build
and height must match before applying it. A replacement build may rewind height;
compare build IDs before comparing heights.

The outbox protects database-to-broker delivery. Core NATS flush is not a client
acknowledgment and does not recover events missed while a client was offline.
Pending outbox rows must be preserved for relay retries. No automatic retention
job is configured; any future cleanup must respect every active destination's
acknowledged cursor. Client recovery uses scoring API data, not this journal.
