package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

const influenceContract = "structs-influence-grass-v1"
const influenceChunkBytes = 4096

// Fixed byte boundaries keep part identity identical during live delivery and
// retries, including reconnects to brokers with different payload limits.
type influencePart struct {
	Contract  string `json:"contract"`
	Subject   string `json:"subject"`
	Category  string `json:"category"`
	FrameID   string `json:"frame_id"`
	PartIndex int    `json:"part_index"`
	PartCount int    `json:"part_count"`
	SHA256    string `json:"sha256"`
	Encoding  string `json:"encoding"`
	Payload   string `json:"payload"`
}

func influenceParts(id int64, subject string, body []byte) ([][]byte, error) {
	sum := sha256.Sum256(body)
	count := (len(body) + influenceChunkBytes - 1) / influenceChunkBytes
	if count == 0 {
		return nil, errors.New("empty influence frame")
	}
	parts := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		end := min((i+1)*influenceChunkBytes, len(body))
		p := influencePart{influenceContract, subject, "influence_part", strconv.FormatInt(id, 10), i, count, hex.EncodeToString(sum[:]), "base64", base64.StdEncoding.EncodeToString(body[i*influenceChunkBytes : end])}
		encoded, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		parts = append(parts, encoded)
	}
	return parts, nil
}

// One publisher per destination owns the advisory session lock. Notifications
// are only wakeups; polling recovers missed LISTENs and relay restarts. No frame
// is skipped for size/errors, and a checkpoint follows the confirmed NATS flush.
func influenceSession(ctx context.Context, cfg *pgx.ConnConfig, host, destination string) error {
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.New("influence database unavailable")
	}
	defer db.Close(context.Background())
	var locked bool
	if err = db.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "structs.grass.delivery."+destination).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("influence destination already has a publisher")
	}
	if _, err = db.Exec(ctx, `INSERT INTO structs.influence_grass_delivery(destination) VALUES($1) ON CONFLICT DO NOTHING`, destination); err != nil {
		return err
	}
	nc, err := nats.Connect(host, nats.NoReconnect(), nats.Timeout(2*time.Second))
	if err != nil {
		return errors.New("influence NATS unavailable")
	}
	defer nc.Close()
	if nc.MaxPayload() < 8192 {
		return errors.New("influence requires NATS max_payload >=8192")
	}
	if _, err = db.Exec(ctx, `LISTEN influence_grass`); err != nil {
		return err
	}
	var cursor int64
	if err = db.QueryRow(ctx, `SELECT last_frame_id FROM structs.influence_grass_delivery WHERE destination=$1`, destination).Scan(&cursor); err != nil {
		return err
	}
	log.Print("influence outbox publisher ready")
	for ctx.Err() == nil {
		// Bound each database read and publication checkpoint during catch-up.
		rows, e := db.Query(ctx, `SELECT frame_id,subject,payload::text FROM structs.influence_grass_frame WHERE frame_id>$1 ORDER BY frame_id LIMIT 32`, cursor)
		if e != nil {
			return e
		}
		type frame struct {
			id            int64
			subject, body string
		}
		batch := []frame{}
		for rows.Next() {
			var f frame
			if e = rows.Scan(&f.id, &f.subject, &f.body); e != nil {
				rows.Close()
				return e
			}
			batch = append(batch, f)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, f := range batch {
			parts, e := influenceParts(f.id, f.subject, []byte(f.body))
			if e != nil {
				return e
			}
			for _, p := range parts {
				if e = nc.Publish(f.subject, p); e != nil {
					return e
				}
			}
		}
		if len(batch) > 0 {
			if e = nc.FlushTimeout(5 * time.Second); e != nil {
				return e
			}
			last := batch[len(batch)-1].id
			if _, e = db.Exec(ctx, `UPDATE structs.influence_grass_delivery SET last_frame_id=$2 WHERE destination=$1`, destination, last); e != nil {
				return e
			}
			cursor = last
		}
		if len(batch) == 0 {
			wake, cancel := context.WithTimeout(ctx, time.Second)
			_, e = db.WaitForNotification(wake)
			cancel()
			if e != nil && !errors.Is(e, context.DeadlineExceeded) {
				return e
			}
		}
		if nc.IsClosed() {
			return errors.New("influence NATS connection closed")
		}
	}
	return ctx.Err()
}
func runInfluence(ctx context.Context, cfg *pgx.ConnConfig, host, destination string) {
	for ctx.Err() == nil {
		err := influenceSession(ctx, cfg, host, destination)
		if ctx.Err() != nil {
			return
		}
		log.Printf("influence delivery paused: %v; retrying", err)
		waitRetry(ctx)
	}
}
