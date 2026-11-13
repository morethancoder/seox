// Package report is seox's area for compare and report.
//
// It owns the ops compare and report. Both call the other areas' Go APIs
// directly (site, signals, rank, mine) and run the independent pieces at the
// same time. A failing piece becomes a warning and an empty section; only a
// main site that cannot be crawled fails the command.
package report
