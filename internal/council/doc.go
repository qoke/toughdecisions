// Package council implements the production pipeline: the live-run
// registry (fan-out of run events) and the runner (parallel views,
// deadline-triggered judge, supersede, rewrite).
package council
