package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"
)

// topicFile holds one topic name and its sample record file.
type topicFile struct {
	topic string
	file  string
}

// topicFileList collects repeated --topic-file flags.
type topicFileList []topicFile

func (l *topicFileList) String() string { return fmt.Sprintf("%v", *l) }

func (l *topicFileList) Set(v string) error {
	parts := strings.SplitN(v, ",", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("value must be in the form topic,path")
	}
	*l = append(*l, topicFile{topic: parts[0], file: parts[1]})
	return nil
}

// config holds the parsed flags.
type config struct {
	cpus            int
	goroutines      int
	pairs           topicFileList
	kafkaURL        string
	minSize         int
	maxSize         int
	reuse           bool
	compressibility float64
	duration        time.Duration
	batchSize       int
	batchBytes      int
	linger          time.Duration
	acks            int
	compression     string
}

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// parseFlags reads the command-line flags. It stops the program if the
// flags are wrong.
func parseFlags() config {
	var cfg config
	flag.IntVar(&cfg.cpus, "cpus", 0, "number of CPUs to use. 0 means use all CPUs")
	flag.IntVar(&cfg.goroutines, "goroutines", 10, "number of producer goroutines per topic")
	flag.Var(&cfg.pairs, "topic-file", "a pair in the form topic,path. Repeat this flag for many topics")
	flag.StringVar(&cfg.kafkaURL, "kafka-url", "", "the Kafka broker address, for example host:9092")
	flag.IntVar(&cfg.minSize, "min-size", 10240, "minimum record size in bytes")
	flag.IntVar(&cfg.maxSize, "max-size", 102400, "maximum record size in bytes")
	flag.BoolVar(&cfg.reuse, "reuse", true, "reuse the record bytes per goroutine. false regenerates each message")
	flag.Float64Var(&cfg.compressibility, "compressibility", 1.0, "target lz4 ratio of the filler. 1 means random and incompressible")
	flag.DurationVar(&cfg.duration, "duration", 0, "run time, for example 30s. 0 means run until stopped")
	flag.IntVar(&cfg.batchSize, "batch-size", 100, "maximum number of messages in one batch")
	flag.IntVar(&cfg.batchBytes, "batch-bytes", 1048576, "maximum number of bytes in one batch")
	flag.DurationVar(&cfg.linger, "linger", 10*time.Millisecond, "time to wait to fill a batch")
	flag.IntVar(&cfg.acks, "acks", 1, "producer acks. 0 none, 1 one, -1 all")
	flag.StringVar(&cfg.compression, "compression", "lz4", "compression: none, gzip, snappy, lz4, zstd")
	flag.Parse()

	if len(cfg.pairs) == 0 {
		exitBadFlag("at least one --topic-file is required")
	}
	if cfg.kafkaURL == "" {
		exitBadFlag("--kafka-url is required")
	}
	if cfg.minSize <= 0 || cfg.maxSize <= 0 {
		exitBadFlag("--min-size and --max-size must be greater than zero")
	}
	if cfg.maxSize < cfg.minSize {
		exitBadFlag("--max-size must not be less than --min-size")
	}
	if cfg.goroutines <= 0 {
		exitBadFlag("--goroutines must be greater than zero")
	}
	return cfg
}

// exitBadFlag prints a flag error and stops the program.
func exitBadFlag(msg string) {
	fmt.Fprintln(os.Stderr, "flag error:", msg)
	flag.Usage()
	os.Exit(2)
}

// run starts the benchmark. It returns when the run stops.
func run(cfg config) error {
	// Set the number of usable CPUs.
	if cfg.cpus > 0 {
		runtime.GOMAXPROCS(cfg.cpus)
	}

	// Build one generation plan per topic before the run starts.
	plans := make([]*node, len(cfg.pairs))
	for i, p := range cfg.pairs {
		plan, err := buildPlan(p.file)
		if err != nil {
			return err
		}
		plans[i] = plan
	}

	// Map the acks flag to the kafka-go type.
	acks, err := requiredAcks(cfg.acks)
	if err != nil {
		return err
	}

	// Map the compression flag to the kafka-go type.
	codec, err := compression(cfg.compression)
	if err != nil {
		return err
	}

	// Stop on a signal. Stop on the duration if the user set one.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.duration)
		defer cancel()
	}

	var totalMsgs int64
	var totalBytes int64

	start := time.Now()
	var wg sync.WaitGroup

	// Start one writer and one goroutine pool per topic.
	for i, p := range cfg.pairs {
		writer := &kafka.Writer{
			Addr:         kafka.TCP(cfg.kafkaURL),
			Topic:        p.topic,
			Balancer:     &kafka.RoundRobin{},
			BatchSize:    cfg.batchSize,
			BatchBytes:   int64(cfg.batchBytes),
			BatchTimeout: cfg.linger,
			RequiredAcks: acks,
			Compression:  codec,
			Async:        false,
		}

		plan := plans[i]
		for gid := 0; gid < cfg.goroutines; gid++ {
			wg.Add(1)
			seed := time.Now().UnixNano() + int64(i*1000+gid)
			go func() {
				defer wg.Done()
				produce(ctx, writer, plan, cfg, seed, &totalMsgs, &totalBytes)
			}()
		}

		// Close the writer after all its goroutines stop.
		defer func(w *kafka.Writer) { _ = w.Close() }(writer)
	}

	wg.Wait()
	elapsed := time.Since(start)

	// Print the minimal exit line. The user measures the rate from outside.
	fmt.Printf("messages=%d bytes=%d elapsed=%s\n",
		atomic.LoadInt64(&totalMsgs), atomic.LoadInt64(&totalBytes), elapsed.Round(time.Millisecond))
	return nil
}

// produce runs the send loop for one goroutine. It stops when the context
// is done.
func produce(ctx context.Context, writer *kafka.Writer, plan *node, cfg config, seed int64, totalMsgs, totalBytes *int64) {
	gen := newGenerator(plan, cfg.minSize, cfg.maxSize, cfg.compressibility, seed)

	// In reuse mode the goroutine builds the record one time.
	var fixed []byte
	if cfg.reuse {
		b, err := gen.record()
		if err != nil {
			fmt.Fprintln(os.Stderr, "generate error:", err)
			return
		}
		fixed = b
	}

	for {
		if ctx.Err() != nil {
			return
		}

		var payload []byte
		if cfg.reuse {
			payload = fixed
		} else {
			b, err := gen.record()
			if err != nil {
				fmt.Fprintln(os.Stderr, "generate error:", err)
				return
			}
			payload = b
		}

		// The message has no key. So messages spread across partitions by
		// round-robin.
		err := writer.WriteMessages(ctx, kafka.Message{Value: payload})
		if err != nil {
			// A canceled context is the normal stop. It is not an error.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			fmt.Fprintln(os.Stderr, "write error:", err)
			continue
		}
		atomic.AddInt64(totalMsgs, 1)
		atomic.AddInt64(totalBytes, int64(len(payload)))
	}
}

// requiredAcks maps the acks flag to the kafka-go type.
func requiredAcks(v int) (kafka.RequiredAcks, error) {
	switch v {
	case 0:
		return kafka.RequireNone, nil
	case 1:
		return kafka.RequireOne, nil
	case -1:
		return kafka.RequireAll, nil
	default:
		return 0, fmt.Errorf("acks must be 0, 1, or -1")
	}
}

// compression maps the compression flag to the kafka-go type.
func compression(name string) (kafka.Compression, error) {
	switch strings.ToLower(name) {
	case "", "none":
		return 0, nil
	case "gzip":
		return kafka.Gzip, nil
	case "snappy":
		return kafka.Snappy, nil
	case "lz4":
		return kafka.Lz4, nil
	case "zstd":
		return kafka.Zstd, nil
	default:
		return 0, fmt.Errorf("compression must be none, gzip, snappy, lz4, or zstd")
	}
}
