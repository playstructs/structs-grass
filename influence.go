package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

const influenceContract = "structs-influence-grass-v1"
const influenceChunkBytes = 4096
const influenceReplayParts = 32

// Fixed byte boundaries keep part identity identical during live delivery and
// replay, including reconnects to brokers with different payload limits.
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

type influenceReplayRequest struct {
	After   string `json:"after_frame_id"`
	Through string `json:"through_frame_id"`
	Part    int    `json:"part_index"`
}
type influenceReplayEnd struct {
	Contract string `json:"contract"`
	Category string `json:"category"`
	Subject  string `json:"subject"`
	After    string `json:"after_frame_id"`
	Through  string `json:"through_frame_id"`
	Part     int    `json:"part_index"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

func exactCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	if strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("invalid cursor")
	}
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil {
		return 0, errors.New("invalid cursor")
	}
	return n, nil
}
func replaySubject(s string) (string, error) {
	if !strings.HasPrefix(s, "structs.influence.replay.") {
		return "", errors.New("invalid replay subject")
	}
	target := "structs.influence." + strings.TrimPrefix(s, "structs.influence.replay.")
	tokens := strings.Split(target, ".")
	if len(tokens) != 5 && len(tokens) != 6 {
		return "", errors.New("invalid replay scope")
	}
	if _, e := exactCursor(tokens[3]); e != nil || tokens[3] == "0" || tokens[3] == "" {
		return "", errors.New("invalid contest")
	}
	scope := tokens[4]
	if (len(tokens) == 5 && scope != "scores" && scope != "contest") || (len(tokens) == 6 && scope != "guild" && scope != "player" && scope != "planet") {
		return "", errors.New("invalid replay scope")
	}
	for _, token := range tokens {
		if token == "" || strings.Trim(token, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
			return "", errors.New("invalid replay token")
		}
	}
	return target, nil
}

// Replies are bounded multipart pages on NATS, not HTTP fetch instructions.
// The first page pins an upper cursor. Subscribe live before replaying and
// buffer later live frames until all pages through that cursor are applied.
func replayInfluence(ctx context.Context, db *pgx.Conn, nc *nats.Conn, m *nats.Msg) error {
	subject, err := replaySubject(m.Subject)
	end := influenceReplayEnd{Contract: influenceContract, Category: "influence_replay_end", Subject: subject}
	sendEnd := func() error { b, _ := json.Marshal(end); return nc.Publish(m.Reply, b) }
	var req influenceReplayRequest
	if err != nil || len(m.Data) > 1024 || json.Unmarshal(m.Data, &req) != nil {
		end.Error = "invalid request"
		return sendEnd()
	}
	after, e1 := exactCursor(req.After)
	through, e2 := exactCursor(req.Through)
	if e1 != nil || e2 != nil || req.Part < 0 {
		end.Error = "invalid cursor"
		return sendEnd()
	}
	var requestedUpper any
	if req.Through != "" {
		requestedUpper = through
	}
	if err = db.QueryRow(ctx, `SELECT coalesce(max(frame_id),0) FROM structs.influence_grass_frame WHERE subject=$1 AND ($2::bigint IS NULL OR frame_id<=$2)`, subject, requestedUpper).Scan(&through); err != nil {
		return err
	}
	if after == through && req.Part != 0 {
		end.Error = "invalid part cursor"
		return sendEnd()
	}
	if after > through {
		end.Error = "cursor is beyond scope watermark"
		return sendEnd()
	}
	// A complete replacement supersedes all earlier state/history. Jump to the
	// latest retained baseline within the pinned range to avoid replaying obsolete
	// generations. Never change the frame underneath a multipart continuation.
	if req.Part == 0 {
		var replacement int64
		if err = db.QueryRow(ctx, `SELECT coalesce(max(frame_id),0) FROM structs.influence_grass_frame
   WHERE subject=$1 AND frame_id>$2 AND frame_id<=$3 AND payload->>'mode'='replace'`, subject, after, through).Scan(&replacement); err != nil {
			return err
		}
		if replacement > 0 {
			after = replacement - 1
		}
	}
	rows, err := db.Query(ctx, `SELECT frame_id,payload::text FROM structs.influence_grass_frame WHERE subject=$1 AND frame_id>$2 AND frame_id<=$3 ORDER BY frame_id LIMIT 33`, subject, after, through)
	if err != nil {
		return err
	}
	defer rows.Close()
	sent := 0
	part := req.Part
	for rows.Next() {
		var id int64
		var body string
		if err = rows.Scan(&id, &body); err != nil {
			return err
		}
		parts, e := influenceParts(id, subject, []byte(body))
		if e != nil {
			return e
		}
		if part >= len(parts) {
			end.Error = "invalid part cursor"
			return sendEnd()
		}
		for part < len(parts) && sent < influenceReplayParts {
			if err = nc.Publish(m.Reply, parts[part]); err != nil {
				return err
			}
			part++
			sent++
		}
		if part < len(parts) {
			break
		}
		after = id
		part = 0
		if sent == influenceReplayParts {
			break
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	end.After = strconv.FormatInt(after, 10)
	end.Through = strconv.FormatInt(through, 10)
	end.Part = part
	end.Done = after == through && part == 0
	return sendEnd()
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
	requests := make(chan *nats.Msg, 64)
	sub, err := nc.Subscribe("structs.influence.replay.>", func(m *nats.Msg) {
		if !strings.HasPrefix(m.Reply, "_INBOX.") || strings.ContainsAny(m.Reply, " *>\t\r\n") {
			return
		}
		select {
		case requests <- m:
		default:
			b, _ := json.Marshal(influenceReplayEnd{Contract: influenceContract, Category: "influence_replay_end", Error: "busy; retry"})
			_ = nc.Publish(m.Reply, b)
		}
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	if err = nc.FlushTimeout(5 * time.Second); err != nil {
		return err
	}
	if _, err = db.Exec(ctx, `LISTEN influence_grass`); err != nil {
		return err
	}
	var cursor int64
	if err = db.QueryRow(ctx, `SELECT last_frame_id FROM structs.influence_grass_delivery WHERE destination=$1`, destination).Scan(&cursor); err != nil {
		return err
	}
	log.Print("influence outbox and replay ready")
	for ctx.Err() == nil {
		// A bounded batch avoids starving reconnect/replay clients during catch-up.
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
		select {
		case m := <-requests:
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			e = replayInfluence(requestCtx, db, nc, m)
			cancel()
			if e != nil {
				return fmt.Errorf("influence replay: %w", e)
			}
		default:
			if len(batch) == 0 {
				// Keep replay latency bounded even when no scores change.
				wake, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				_, e = db.WaitForNotification(wake)
				cancel()
				if e != nil && !errors.Is(e, context.DeadlineExceeded) {
					return e
				}
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
