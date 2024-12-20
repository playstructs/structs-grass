package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
//	"time"

	"github.com/brojonat/notifier/notifier"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nats-io/nats.go"
	"github.com/tidwall/gjson"
)

/* 
 * Initial code provided by Jon Brown 
 * https://brojonat.com/posts/go-postgres-listen-notify/
 */

func main() {

	ctx := context.Background()
	l := getDefaultLogger(slog.LevelInfo)

	var url string
	flag.StringVar(&url, "dbhost", "", "DB host (postgresql://{user}:{password}@{hostname}/{db}?sslmode=require)")

	var topic string
	flag.StringVar(&topic, "channel", "", "a string")

	var natHost string
	flag.StringVar(&natHost, "nathost", "", "NAT host (nats://{hostname}:{port})") // default port 4222


	flag.Parse()

	if url == "" || topic == "" || natHost == "" {
		fmt.Fprintf(os.Stderr, "missing required flag")
		os.Exit(1)
		return
	}

	fmt.Printf("Channel: %s\n", topic)
	
	fmt.Printf("Connecting to NATS: %s \n", natHost)
	nc, _ := nats.Connect(natHost)


	// get a connection pool
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connection to DB: %v", err)
		os.Exit(1)
	}
	if err = pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error pinging DB: %v", err)
		os.Exit(1)
	}

	// setup the listener
	li := notifier.NewListener(pool)
	if err := li.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error setting up listener: %v", err)
		os.Exit(1)
	}

	// setup the notifier
	n := notifier.NewNotifier(l, li)
	go n.Run(ctx)

	// subscribe to the topic
	sub := n.Listen(topic)

	// indefinitely listen for updates
	go func() {
		<-sub.EstablishedC()
		for {
			select {
			case <-ctx.Done():
				sub.Unlisten(ctx)
				fmt.Println("done listening for notifications")
				return
			case p := <-sub.NotificationC():
				//fmt.Printf("Got notification: %s\n", p)
				subject := gjson.Get(string(p), "subject") 
				nc.Publish(subject.String(), []byte(p))
			}
		}
	}()

	// unsubscribe after some time
	/*
	go func() {
		time.Sleep(20 * time.Second)
		sub.Unlisten(ctx)
	}()
	*/

	select {}
}

func getDefaultLogger(lvl slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     lvl,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.SourceKey {
				source, _ := a.Value.Any().(*slog.Source)
				if source != nil {
					source.Function = ""
					source.File = filepath.Base(source.File)
				}
			}
			return a
		},
	}))
}

