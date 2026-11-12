package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

func TestInfluenceParts(t *testing.T) {
	body := []byte(strings.Repeat("\"星🌌\"", 5000))
	parts, e := influenceParts(9007199254740993, "structs.influence.test.1.scores", body)
	if e != nil {
		t.Fatal(e)
	}
	var joined []byte
	for i, b := range parts {
		if len(b) >= 8192 {
			t.Fatal("unbounded packet", len(b))
		}
		var p influencePart
		if e = json.Unmarshal(b, &p); e != nil {
			t.Fatal(e)
		}
		if p.FrameID != "9007199254740993" || p.PartIndex != i || p.PartCount != len(parts) {
			t.Fatal(p)
		}
		raw, e := base64.StdEncoding.DecodeString(p.Payload)
		if e != nil {
			t.Fatal(e)
		}
		joined = append(joined, raw...)
		sum := sha256.Sum256(body)
		if p.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("digest")
		}
	}
	if !bytes.Equal(joined, body) {
		t.Fatal("multipart bytes changed")
	}
}
func TestIntegrationInfluenceOutboxReadOnlyClients(t *testing.T) {
	dsn, host := os.Getenv("GRASS_TEST_DATABASE_URL"), os.Getenv("GRASS_TEST_NATS_URL")
	if dsn == "" || host == "" {
		t.Skip("disposable PG/NATS required")
	}
	ctx := context.Background()
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	db, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close(ctx)
	readerHost := os.Getenv("GRASS_TEST_NATS_READER_URL")
	if readerHost == "" {
		t.Fatal("read-only NATS fixture URL required")
	}
	nc, e := nats.Connect(readerHost)
	if e != nil {
		t.Fatal(e)
	}
	defer nc.Close()
	subject := "structs.influence.relay-test.999999.scores"
	dest := fmt.Sprintf("test-%d", time.Now().UnixNano())
	live, e := nc.SubscribeSync(subject)
	if e != nil {
		t.Fatal(e)
	}
	defer live.Unsubscribe()
	if e = nc.Flush(); e != nil {
		t.Fatal(e)
	}
	var id int64
	// A genuinely oversized frame arrives completely through a read-only subscription.
	body := map[string]any{"contract": influenceContract, "subject": subject, "category": "influence_scores", "mode": "replace", "previous_frame_id": "0", "data": strings.Repeat("星🌌", 40000)}
	raw, _ := json.Marshal(body)
	if e = db.QueryRow(ctx, `INSERT INTO structs.influence_grass_frame(contest_id,build_id,height,subject,payload) VALUES(999999,999999,1,$1,$2) RETURNING frame_id`, subject, raw).Scan(&id); e != nil {
		t.Fatal(e)
	}
	body["frame_id"] = strconv.FormatInt(id, 10)
	raw, _ = json.Marshal(body)
	if _, e = db.Exec(ctx, `UPDATE structs.influence_grass_frame SET payload=$2 WHERE frame_id=$1`, id, raw); e != nil {
		t.Fatal(e)
	}
	// Skip unrelated fixture frames for this independent destination.
	if _, e = db.Exec(ctx, `INSERT INTO structs.influence_grass_delivery VALUES($1,$2)`, dest, id-1); e != nil {
		t.Fatal(e)
	}
	start := func() (context.CancelFunc, <-chan error) {
		c, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- influenceSession(c, cfg, host, dest) }()
		return cancel, done
	}
	cancel, done := start()
	defer func() {
		if done != nil {
			cancel()
			<-done
		}
	}()
	var canonical string
	if e = db.QueryRow(ctx, `SELECT payload::text FROM structs.influence_grass_frame WHERE frame_id=$1`, id).Scan(&canonical); e != nil {
		t.Fatal(e)
	}
	expected, _ := influenceParts(id, subject, []byte(canonical))
	for _, part := range expected {
		m, e := live.NextMsg(10 * time.Second)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(m.Data, part) {
			t.Fatal("live payload changed")
		}
	}
	for i := 0; i < 100; i++ {
		var cursor int64
		_ = db.QueryRow(ctx, `SELECT last_frame_id FROM structs.influence_grass_delivery WHERE destination=$1`, dest).Scan(&cursor)
		if cursor == id {
			break
		}
		time.Sleep(10 * time.Millisecond)
		if i == 99 {
			t.Fatal("checkpoint not advanced")
		}
	}
	// Restarting after an acknowledged flush must not resend delivered frames.
	cancel()
	<-done
	done = nil
	cancel, done = start()
	if _, e = live.NextMsg(300 * time.Millisecond); e != nats.ErrTimeout {
		t.Fatalf("unexpected resend: %v", e)
	}
	// Delivery outage: a committed frame without NOTIFY survives and is found on restart.
	cancel()
	<-done
	done = nil
	if _, e = db.Exec(ctx, `INSERT INTO structs.influence_grass_frame(contest_id,build_id,height,subject,payload) VALUES(999999,999999,2,$1,'{"offline":true}')`, subject); e != nil {
		t.Fatal(e)
	}
	cancel, done = start()
	if _, e = live.NextMsg(10 * time.Second); e != nil {
		t.Fatal("missed-notify recovery", e)
	}
}
