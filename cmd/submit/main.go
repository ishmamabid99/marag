// Command submit kicks off a run headlessly by publishing a run.requested event
// (the same path the MCP/Linear gateways use), then prints the run id. Handy for
// a preview/smoke without wiring the MCP client or a Linear webhook.
//
//	submit -spec "add a /health endpoint" -repos repo-a,repo-b
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/gateway"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

func main() {
	spec := flag.String("spec", "", "feature request text (required)")
	reposCSV := flag.String("repos", "", "comma-separated candidate repo ids (optional)")
	flag.Parse()
	if *spec == "" {
		log.Fatal("usage: submit -spec \"<feature request>\" [-repos a,b]")
	}

	cfg := config.Get()
	brokers := cfg.Kafka.Brokers
	if len(brokers) == 0 {
		log.Fatal("kafka.brokers is required")
	}
	prod, err := kafka.NewProducer(brokers, kafka.WithAuth(kafka.ClientAuth{
		SASLUser:    cfg.Kafka.Username,
		SASLPass:    cfg.Kafka.Password,
		TLSEnabled:  cfg.Kafka.TLSEnabled,
		Compression: cfg.Kafka.ProducerCompression,
		MaxRetries:  cfg.Kafka.ProducerMaxRetries,
	}))
	if err != nil {
		log.Fatalf("producer: %v", err)
	}
	defer prod.Close()

	var repos []string
	for _, r := range strings.Split(*reposCSV, ",") {
		if r = strings.TrimSpace(r); r != "" {
			repos = append(repos, r)
		}
	}

	id, err := gateway.New(prod, nil, nil, nil).SubmitRun(context.Background(), *spec, repos)
	if err != nil {
		log.Fatalf("submit: %v", err)
	}
	fmt.Printf("submitted run_id=%s\n", id)
}
