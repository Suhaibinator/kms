package cli

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// outputMode selects how a command renders its result. It implements
// flag.Value so the same parser serves --output before and after the
// subcommand.
type outputMode string

const (
	outputTable outputMode = "table"
	outputJSON  outputMode = "json"
)

func (m *outputMode) String() string {
	if *m == "" {
		return string(outputTable)
	}
	return string(*m)
}

func (m *outputMode) Set(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "table":
		*m = outputTable
	case "json":
		*m = outputJSON
	default:
		return fmt.Errorf("invalid output format %q (want table or json)", v)
	}
	return nil
}

// jsonOutput reports whether the command should print JSON instead of the
// human table. In JSON mode stdout carries exactly one document; anything
// meant for a person goes to stderr through info.
func (c *CLI) jsonOutput() bool { return c.output == outputJSON }

// printTable renders headers and rows as the tab-aligned table list commands
// have always printed (tabwriter with tabwidth 4, padding 2), so scripts that
// parse the human output keep working byte for byte.
func (c *CLI) printTable(headers []string, rows [][]string) {
	writeAlignedTable(c.Stdout, headers, rows)
}

// writeAlignedTable is printTable for an arbitrary writer. Release commands
// need it because the same tables also go to stderr (the activation preview,
// a failed activation's validation errors) and to test buffers.
func writeAlignedTable(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
}

// printJSON writes v to stdout as one indented, deterministic JSON document
// and returns the exit code. Field names are snake_case, timestamps are
// RFC 3339 in UTC (see jsonTime), absent objects are null, and lists are
// never null.
func (c *CLI) printJSON(v any) int {
	if err := writeJSON(c.Stdout, v); err != nil {
		return c.fail("encoding json output: %v", err)
	}
	return exitOK
}

// listPage is the JSON envelope shared by every list command. Commands that
// page through the whole result set leave next_page_token empty.
type listPage struct {
	Items         any    `json:"items"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// printList writes a list result in the shared envelope. items must be a
// non-nil slice so an empty result renders as [] rather than null.
func (c *CLI) printList(items any, nextPageToken string) int {
	return c.printJSON(listPage{Items: items, NextPageToken: nextPageToken})
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// jsonTime renders a Unix-millisecond timestamp for JSON output: RFC 3339 in
// UTC with nanosecond precision, or nil (JSON null) for the zero value the
// wire uses to mean "never" or "not yet".
func jsonTime(unixMs int64) *string {
	if unixMs == 0 {
		return nil
	}
	s := time.UnixMilli(unixMs).UTC().Format(time.RFC3339Nano)
	return &s
}

// jsonTimeOf renders a time.Time the same way; the zero time is null.
func jsonTimeOf(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}
