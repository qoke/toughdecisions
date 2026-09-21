package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"
)

// feedbackSummary prints per-tag feedback counts for the last N days.
func feedbackSummary(args []string) int {
	fs := flag.NewFlagSet("feedback summary", flag.ContinueOnError)
	days := fs.Int("days", 7, "look back N days")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if *days < 0 {
		fmt.Fprintf(os.Stderr, "feedback summary: --days must be >= 0\n")
		return exitValidation
	}
	db, _, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "feedback summary: %v\n", err)
		return exitError
	}
	defer db.Close()
	since := time.Now().UTC().AddDate(0, 0, -*days).Format(time.RFC3339)
	counts, err := db.FeedbackTagCounts(since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "feedback summary: %v\n", err)
		return exitError
	}
	tags := make([]string, 0, len(counts))
	total := 0
	for tag, n := range counts {
		tags = append(tags, tag)
		total += n
	}
	sort.Strings(tags)
	fmt.Printf("feedback summary: last %d days, %d total\n", *days, total)
	for _, tag := range tags {
		fmt.Printf("  %s: %d\n", tag, counts[tag])
	}
	return exitOK
}
