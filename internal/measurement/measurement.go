// Package measurement derives benchmark metrics from the ground-truth event
// log and monitor reports stored in the database.
//
// Metric derivation is always a separate pass from raw event writes —
// never in the same transaction. All metrics are recomputable from the
// raw tables at any time.
package measurement

// TODO: implement metric derivation
//
// Metrics to derive per (run, service) pair:
//
//   detection_latency_s   float64  seconds from failure_start to first alert_fired
//   true_positive         bool     alert_fired during active failure window
//   false_negative        bool     RetrieveKnown, no alert_fired during failure
//   false_positive        bool     alert_fired with no active failure (passive detection)
//   unknown               bool     RetrieveUnknown for any reason
//   capability_skip       bool     pair skipped before provisioning (capability mismatch)
//   classification_score  float64  tiered score for failure type classification accuracy
