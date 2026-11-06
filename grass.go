package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

const (
	queueSize  = 1024
	batchSize  = 128
	retryDelay = 2 * time.Second
)

type message struct {
	subject string
	body    []byte
}

func parseMessage(payload string) (message, error) {
	var envelope struct {
		Subject string `json:"subject"`
	}
	body := []byte(payload)
	if err := json.Unmarshal(body, &envelope); err != nil {
		return message{}, err
	}
	s := envelope.Subject
	if s == "" || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") ||
		strings.Contains(s, "..") || strings.ContainsAny(s, " \t\r\n*>\x00") {
		return message{}, errors.New("invalid NATS publish subject")
	}
	return message{subject: s, body: body}, nil
}

func listen(ctx context.Context, cfg *pgx.ConnConfig, channel string, out chan<- message) error {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		// Connection errors may include the DSN.
		return errors.New("could not connect to PostgreSQL")
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return fmt.Errorf("LISTEN: %w", err)
	}
	log.Printf("listening on PostgreSQL channel %q", channel)
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		m, err := parseMessage(n.Payload)
		if err != nil {
			log.Printf("skipping malformed notification: %v", err)
			continue
		}
		select {
		case out <- m:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func waitRetry(ctx context.Context) {
	select {
	case <-time.After(retryDelay):
	case <-ctx.Done():
	}
}

func publishBatch(nc *nats.Conn, batch []message) error {
	for _, m := range batch {
		if err := nc.Publish(m.subject, m.body); err != nil {
			if errors.Is(err, nats.ErrMaxPayload) {
				log.Printf("skipping notification larger than broker limit (%d bytes)", len(m.body))
				continue
			}
			return err
		}
	}
	return nc.FlushTimeout(5 * time.Second)
}

func publish(ctx context.Context, host string, in <-chan message) {
	var nc *nats.Conn
	defer func() {
		if nc != nil {
			nc.Close()
		}
	}()
	for first := range in {
		// This is the sole consumer. Take only messages already queued so an
		// isolated update is sent immediately.
		available := min(len(in), batchSize-1)
		batch := make([]message, 1, 1+available)
		batch[0] = first
		for range available {
			batch = append(batch, <-in)
		}
		retrying := false
		for {
			if ctx.Err() != nil {
				log.Printf("shutdown with %d unconfirmed or queued notifications", len(batch)+len(in))
				return
			}
			var err error
			if nc == nil {
				// We retain the batch ourselves. Never let a reconnect move a
				// flush onto a different connection than its publishes.
				nc, err = nats.Connect(host, nats.NoReconnect(), nats.Timeout(2*time.Second))
				if err != nil {
					err = errors.New("could not connect to NATS")
				}
			}
			if err == nil {
				err = publishBatch(nc, batch)
			}
			if err == nil {
				if retrying {
					log.Print("NATS delivery resumed")
				}
				break
			}
			if nc != nil {
				nc.Close()
				nc = nil
			}
			if !retrying {
				log.Printf("NATS delivery interrupted: %v; retrying batch of %d", err, len(batch))
				retrying = true
			}
			waitRetry(ctx)
		}
	}
}

func main() {
	url := flag.String("dbhost", "", "PostgreSQL connection string")
	channel := flag.String("channel", "", "PostgreSQL notification channel")
	host := flag.String("nathost", "", "NATS URL")
	flag.Parse()
	if *url == "" || *channel == "" || *host == "" {
		log.Fatal("dbhost, channel and nathost are required")
	}
	cfg, err := pgx.ParseConfig(*url)
	if err != nil {
		log.Fatal("invalid PostgreSQL connection string")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	out := make(chan message, queueSize)
	writerCtx, cancelWriter := context.WithCancel(context.Background())
	defer cancelWriter()
	go func() {
		defer close(out)
		for ctx.Err() == nil {
			err := listen(ctx, cfg, *channel, out)
			if ctx.Err() != nil {
				break
			}
			log.Printf("PostgreSQL listener unavailable: %v; resubscribing", err)
			waitRetry(ctx)
		}
	}()
	go func() {
		<-ctx.Done()
		// Allow already received messages to drain within Docker's stop timeout.
		time.AfterFunc(5*time.Second, cancelWriter)
	}()
	publish(writerCtx, *host, out)
}
