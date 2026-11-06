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
go build grass.go  
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
