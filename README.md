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

## Learn more

- [Structs](https://playstructs.com)
- [Project Wiki](https://watt.wiki)
- [@PlayStructs Twitter](https://twitter.com/playstructs)


## License

Most of this was written by Jon Brown in this [blog post](https://brojonat.com/posts/go-postgres-listen-notify/), so it doesn't feel right to plaster a license on.
