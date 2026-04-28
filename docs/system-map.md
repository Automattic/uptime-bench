# uptime-bench System Map

This map shows the moving parts in uptime-bench, what each part is for, and
how the pieces communicate during single-scenario and campaign runs.

## Component Map

```mermaid
flowchart LR
  Operator["Operator / CI"]
  ScenarioFiles["Scenario TOML<br/>scenarios/*.toml"]
  CampaignConfig["Campaign TOML"]
  FleetConfig["fleet.toml"]
  ServiceConfig["services.toml"]
  MySQL[("MySQL<br/>event log + metrics")]

  subgraph Harness["Harness process"]
    HarnessCLI["cmd/harness"]
    ScenarioParser["internal/scenario<br/>parse + validate"]
    CampaignEngine["internal/campaign<br/>generate schedule"]
    Runner["internal/runner<br/>execute runs"]
    AdapterIface["adapter.Adapter<br/>common contract"]
    Measurement["internal/measurement<br/>derive metrics"]
    JetmonV1["jetmon-v1 adapter"]
    JetmonV2["jetmon-v2 adapter"]
    ProbeAdapters["probe adapters<br/>Pingdom, UptimeRobot,<br/>Datadog, Better Uptime"]
  end

  subgraph Fleet["Controlled uptime-bench fleet"]
    subgraph TargetVM["Target VM - cmd/target"]
      TargetControl["control API<br/>POST /activate<br/>POST /deactivate<br/>GET /status<br/>PUT /config/cert-library"]
      FailureRegistry["failure registry<br/>active host/path states"]
      TCPProxy["TCP proxy layer<br/>tcp_refused, tcp_timeout"]
      TLSLayer["TLS/SNI layer<br/>tls_* failures + cert selection"]
      HTTPHandler["HTTP handler<br/>http_* and content failures"]
    end

    subgraph DNSVM["DNS VM - cmd/dns"]
      DNSControl["control API<br/>failure control + ACME TXT"]
      AuthoritativeDNS["authoritative DNS<br/>dns_* failures"]
    end

    subgraph CertmintVM["Certmint VM - cmd/certmint"]
      Certmint["certmint daemon<br/>issue + archive certs"]
      CertLibrary["cert library HTTP API<br/>manifest.json + PEM files"]
    end
  end

  subgraph External["Monitoring services under test"]
    Jetmon1["Jetmon 1<br/>via jetmon-bridge"]
    Jetmon2["Jetmon 2<br/>REST API"]
    Pingdom["Pingdom API"]
    UptimeRobot["UptimeRobot API"]
    Datadog["Datadog Synthetics API"]
    BetterUptime["Better Uptime API"]
    Probes["vendor probe workers<br/>HTTP / TCP / TLS / DNS checks"]
  end

  ReportTool["cmd/uptime-bench-report"]
  Reports["table / TSV / JSON reports"]

  Operator -->|"runs CLI"| HarnessCLI
  ScenarioFiles -->|"single run input"| HarnessCLI
  CampaignConfig -->|"campaign input"| HarnessCLI
  FleetConfig -->|"fleet members, domains,<br/>control addresses"| HarnessCLI
  ServiceConfig -->|"enabled services,<br/>credentials, budgets"| HarnessCLI

  HarnessCLI --> ScenarioParser
  HarnessCLI --> CampaignEngine
  CampaignEngine -->|"scheduled replay plans"| Runner
  ScenarioParser -->|"validated scenario"| Runner
  Runner --> AdapterIface
  AdapterIface --> JetmonV1
  AdapterIface --> JetmonV2
  AdapterIface --> ProbeAdapters

  Runner -->|"write scenario_runs,<br/>ground_truth_events,<br/>monitor_reports"| MySQL
  Runner -->|"authenticated HTTP control"| TargetControl
  Runner -->|"authenticated HTTP control"| DNSControl
  TargetControl --> FailureRegistry
  DNSControl --> AuthoritativeDNS
  FailureRegistry --> TCPProxy
  FailureRegistry --> TLSLayer
  FailureRegistry --> HTTPHandler

  JetmonV1 -->|"provision / retrieve / deprovision"| Jetmon1
  JetmonV2 -->|"provision / retrieve / deprovision"| Jetmon2
  ProbeAdapters -->|"provision / retrieve / deprovision"| Pingdom
  ProbeAdapters -->|"provision / retrieve / deprovision"| UptimeRobot
  ProbeAdapters -->|"provision / retrieve / deprovision"| Datadog
  ProbeAdapters -->|"provision / retrieve / deprovision"| BetterUptime

  Jetmon1 --> Probes
  Jetmon2 --> Probes
  Pingdom --> Probes
  UptimeRobot --> Probes
  Datadog --> Probes
  BetterUptime --> Probes

  Probes -->|"DNS queries"| AuthoritativeDNS
  Probes -->|"HTTP probes"| HTTPHandler
  Probes -->|"HTTPS probes"| TLSLayer
  Probes -->|"TCP probes"| TCPProxy

  Certmint -->|"certbot DNS-01 hooks<br/>PUT/DELETE /acme/txt"| DNSControl
  Certmint -->|"publishes"| CertLibrary
  HarnessCLI -->|"push cert-library URL"| TargetControl
  TLSLayer -->|"polls / loads certs"| CertLibrary

  Measurement -->|"read raw events"| MySQL
  Measurement -->|"write derived_metrics"| MySQL
  Runner --> Measurement
  ReportTool -->|"read campaign_runs,<br/>derived_metrics,<br/>reason_code rows"| MySQL
  ReportTool --> Reports
```

## Scenario Run Sequence

```mermaid
sequenceDiagram
  autonumber
  participant Op as Operator / CI
  participant H as cmd/harness
  participant R as internal/runner
  participant DB as MySQL
  participant A as adapter
  participant S as monitoring service API
  participant C as target / DNS control API
  participant P as vendor probes
  participant T as target HTTP/TCP/TLS + DNS
  participant M as measurement engine

  Op->>H: start with -scenario or -campaign
  H->>H: load fleet.toml + services.toml
  H->>H: parse scenario or generate campaign schedule
  H->>R: execute one replay
  R->>DB: insert scenario_runs
  R->>A: Capabilities()
  alt capability mismatch
    R->>DB: insert monitor_reports reason_code=capability_mismatch
  else compatible service
    R->>A: Provision(target, check config)
    A->>S: create monitor / maintenance / keyword config
  end
  R->>C: POST /activate failure
  R->>DB: insert ground_truth_events failure_start
  P->>T: probe target through real DNS, TCP, TLS, HTTP
  T-->>P: healthy or injected failure response
  P->>S: record incident state
  R->>C: POST /deactivate failure
  R->>DB: insert ground_truth_events failure_end
  R->>R: wait grace period
  R->>A: Retrieve(run window)
  A->>S: fetch incidents / events
  A-->>R: known reports or Unknown reason
  R->>DB: insert monitor_reports
  R->>A: Deprovision(handle)
  A->>S: remove monitor and cleanup state
  R->>DB: close scenario_runs
  R->>M: derive metrics for run or campaign
  M->>DB: read ground truth + monitor reports
  M->>DB: upsert derived_metrics
```

## Data And Reporting Flow

```mermaid
flowchart TB
  ScenarioRun["scenario_runs<br/>one row per replay"]
  CampaignRun["campaign_runs<br/>one row per campaign execution"]
  GroundTruth["ground_truth_events<br/>what uptime-bench injected"]
  MonitorReports["monitor_reports<br/>what services reported<br/>plus reason_code"]
  Measurement["internal/measurement"]
  Derived["derived_metrics<br/>true_positive, false_negative,<br/>false_positive, unknown,<br/>maintenance_suppressed,<br/>latency"]
  ReportDBQuery["report DB queries<br/>campaign run IDs + reason rows"]
  ReportSummary["internal/report<br/>group by failure_type + service"]
  BiasChecks["bias checks<br/>sample balance, missing cells,<br/>capability mismatch,<br/>uncategorized Unknown"]
  ReportOutput["table / TSV / JSON"]

  CampaignRun --> ScenarioRun
  ScenarioRun --> GroundTruth
  ScenarioRun --> MonitorReports
  GroundTruth --> Measurement
  MonitorReports --> Measurement
  Measurement --> Derived
  CampaignRun --> ReportDBQuery
  ScenarioRun --> ReportDBQuery
  Derived --> ReportDBQuery
  MonitorReports --> ReportDBQuery
  ReportDBQuery --> ReportSummary
  ReportSummary --> BiasChecks
  ReportSummary --> ReportOutput
  BiasChecks --> ReportOutput
```

## Component Responsibilities

| Part | Purpose | Main communication |
|---|---|---|
| `cmd/harness` | CLI entry point. Loads config, constructs adapters, pushes cert-library config, and starts single-scenario or campaign execution. | Reads TOML files; calls runner; talks to target control APIs for cert-library setup. |
| `internal/scenario` | Parses and validates scenario TOML. Defines target, failures, timing, maintenance, keywords, and monitors. | In-process Go calls from the harness and runner. |
| `internal/campaign` | Generates deterministic randomized campaign designs and schedules from a campaign config and seed. | In-process Go calls; campaign audit rows go to MySQL through the runner. |
| `internal/runner` | Orchestrates each replay: provision monitors, activate failures, record ground truth, retrieve reports, deprovision, and close runs. | MySQL writes; adapter interface calls; authenticated HTTP control calls to fleet members. |
| `adapter.Adapter` implementations | Hide service-specific API behavior behind one contract: capabilities, provision, retrieve, normalize, deprovision. | HTTPS calls to vendor APIs or Jetmon bridge/API. |
| Monitoring services | The systems under evaluation. They run checks and expose incident data through their APIs. | Vendor probes hit target fleet data-plane endpoints; adapters use service APIs. |
| Target VM (`cmd/target`) | Hosts benchmark websites and injects TCP, TLS, HTTP, redirect, timeout, status, and content failures. | Data plane: HTTP/HTTPS/TCP from vendor probes. Control plane: authenticated HTTP from harness. |
| DNS VM (`cmd/dns`) | Authoritative DNS for benchmark domains and DNS failure injection. Also accepts ACME TXT updates for certmint. | DNS from vendor resolvers/probes; authenticated HTTP control from harness and certmint hooks. |
| Certmint (`cmd/certmint`) | Maintains a library of real certificates at different ages for TLS expiry/expiring tests. | Drives DNS-01 via DNS VM control endpoints; serves manifest and PEM files over a read-only HTTP API. |
| MySQL | Canonical event log and metric store. Raw rows are retained so metrics can be recomputed. | SQL from harness, measurement, and report tool. |
| `internal/measurement` | Converts raw ground-truth and monitor-report rows into comparable derived metrics. | SQL reads from raw tables; SQL upserts to `derived_metrics`. |
| `cmd/uptime-bench-report` | Builds human and machine-readable campaign summaries. | SQL reads from campaign, metric, and reason-code rows; writes table, TSV, or JSON to stdout. |

## Communication Boundaries

| Boundary | Protocol | Why it exists |
|---|---|---|
| Harness to fleet control plane | Authenticated HTTP/JSON on dedicated control ports | Keeps failure activation separate from public data-plane traffic. |
| Vendor probes to target fleet | Real DNS, TCP, TLS, and HTTP(S) | Ensures monitors experience the target like a real external website. |
| Adapters to monitoring services | Service-specific HTTPS APIs or Jetmon bridge/API | Encapsulates vendor API differences inside adapters. |
| Certmint to DNS VMs | Authenticated HTTP for ACME TXT records | Lets certbot complete DNS-01 challenges using the same authoritative DNS fleet. |
| Target TLS layer to cert library | HTTP poll plus local cache | Lets targets serve certificate-age scenarios without minting certs during a benchmark run. |
| Core processes to MySQL | SQL | Preserves raw events and derived metrics for audit, recomputation, and reporting. |
