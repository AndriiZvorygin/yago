package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(execute())
}

func execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, progress io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: yacy-migrate export|import [options]")
	}
	switch args[0] {
	case "export":
		flags := flag.NewFlagSet("export", flag.ContinueOnError)
		flags.SetOutput(progress)
		opts := exportOptions{}
		flags.StringVar(&opts.SolrURL, "solr-url", "", "read-only YaCy Solr select endpoint")
		flags.StringVar(&opts.Output, "output", "", "private JSONL output path")
		flags.StringVar(
			&opts.SourceID,
			"source-id",
			"",
			"non-sensitive stable identifier for this export",
		)
		flags.IntVar(&opts.Rows, "rows", 16, "bounded Solr page size, 1 through 64")
		flags.BoolVar(&opts.DryRun, "dry-run", false, "query source count only; do not write files")
		if err := flags.Parse(args[1:]); err != nil {
			return fmt.Errorf("parse export options: %w", err)
		}
		opts.User = os.Getenv("YACY_USERNAME")
		opts.Password = os.Getenv("YACY_PASSWORD")
		return exportCorpus(ctx, opts, progress)
	case "import":
		flags := flag.NewFlagSet("import", flag.ContinueOnError)
		flags.SetOutput(progress)
		opts := importOptions{}
		flags.StringVar(&opts.Input, "input", "", "versioned YaCy JSONL interchange file")
		flags.StringVar(&opts.Endpoint, "endpoint", "", "Yago POST /admin/index/import endpoint")
		flags.StringVar(&opts.State, "state", "", "checkpoint path; defaults beside input")
		flags.BoolVar(
			&opts.DryRun,
			"dry-run",
			false,
			"validate remaining input without sending or checkpointing",
		)
		if err := flags.Parse(args[1:]); err != nil {
			return fmt.Errorf("parse import options: %w", err)
		}
		opts.APIKey = os.Getenv("YAGO_API_KEY")
		return importCorpus(ctx, opts, progress)
	default:
		return fmt.Errorf("unknown command %q; use export or import", args[0])
	}
}
