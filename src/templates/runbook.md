# Runbook: {cell-id}

<!-- Operations runbook — reference: Google SRE Book, Chapter 14 (Managing Incidents).
     Keep this document up to date as the Cell evolves. -->

## Cell Overview

| Field              | Value                                                    |
|--------------------|----------------------------------------------------------|
| Cell ID            | {cell-id}                                                |
| Type               | {core / edge / support}                                  |
| Consistency Level  | {L0-L4}                                                  |
| Owner / On-call    | {team or individual}                                     |
| Assembly           | {assembly name}                                          |
| Repository         | {repo URL or path}                                       |

### Architecture Summary

<!-- Brief description of what this Cell does and its critical dependencies. -->

{1-2 paragraph summary of the Cell's purpose and how it fits into the system.}

### Dependency Map

| Dependency         | Type              | Impact if Unavailable              |
|--------------------|-------------------|------------------------------------|
| {PostgreSQL}       | {datastore}       | {Cell cannot read/write data}      |
| {RabbitMQ}         | {message broker}  | {Events queued locally via outbox}  |
| {cell-id}          | {contract: sync}  | {Feature X degraded}               |
| {Redis}            | {cache}           | {Degraded performance, still functional} |

---

## Health Check Endpoints

<!-- List all health and readiness endpoints. -->

| Endpoint           | Method | Expected Response    | Meaning                          |
|--------------------|--------|----------------------|----------------------------------|
| `/healthz`         | GET    | `200 OK`             | Process is alive                 |
| `/readyz`          | GET    | `200 OK`             | Ready to accept traffic; aggregate status only |
| `/readyz?verbose`  | GET    | `200` + JSON details | Per-dependency health status     |

`/readyz` is the safe default probe for load balancers and orchestrators. Use `/readyz?verbose` only when an operator needs cell and dependency breakdown.

### Health Check Verification

```bash
# Quick health check
curl -sf http://{host}:{port}/healthz

# Detailed readiness check
curl -sf http://{host}:{port}/readyz?verbose | jq .
```

---

## Common Issues and Resolution

### Issue 1: {Database Connection Failures}

**Symptoms:**
- {Error logs: "failed to connect to database"}
- {Health endpoint /readyz returns 503}
- {Spike in ERR_* error codes in metrics}

**Diagnosis:**
```bash
# Check database connectivity
{diagnostic command, e.g. pg_isready -h $DB_HOST -p $DB_PORT}

# Check connection pool metrics
{curl command to check metrics endpoint}

# Check recent error logs
{kubectl logs or journalctl command}
```

**Resolution:**
1. {Step 1: Verify database host is reachable}
2. {Step 2: Check connection pool limits and active connections}
3. {Step 3: Restart the Cell pod if connection pool is exhausted}
4. {Step 4: Escalate to DBA if database is unresponsive}

### Issue 2: {Event Processing Lag}

**Symptoms:**
- {Outbox relay lag metric increasing}
- {Consumer group lag > threshold}
- {Downstream Cells see stale data}

**Diagnosis:**
```bash
# Check outbox table pending count
{SQL query or metrics check}

# Check consumer group lag
{broker CLI command}
```

**Resolution:**
1. {Step 1: Check if the outbox relay process is running}
2. {Step 2: Check message broker health}
3. {Step 3: Check for poison messages in dead letter queue}
4. {Step 4: Scale outbox relay workers if throughput is the bottleneck}

### Issue 3: {High Latency}

**Symptoms:**
- {p95 latency > SLO threshold}
- {Timeout errors in upstream Cells}

**Diagnosis:**
```bash
# Check current latency percentiles
{curl metrics endpoint or Grafana query}

# Check for slow queries
{database slow query log command}
```

**Resolution:**
1. {Step 1: Identify slow endpoints from metrics}
2. {Step 2: Check for missing indexes or query plan changes}
3. {Step 3: Check resource utilization (CPU, memory, disk I/O)}
4. {Step 4: Scale horizontally if resource-bound}

<!-- Add more issues as they are discovered in production. -->

---

## Rollback Procedure

### Prerequisites

- [ ] Access to deployment tooling (CI/CD pipeline or kubectl)
- [ ] Knowledge of the last known good version
- [ ] Notification sent to on-call channel

### Steps

1. **Identify the target rollback version:**
   ```bash
   # List recent deployments
   {kubectl rollout history deployment/{cell-id} or CI/CD command}
   ```

2. **Execute rollback:**
   ```bash
   # Roll back to previous revision
   {kubectl rollout undo deployment/{cell-id} or CI/CD command}
   ```

3. **Verify rollback success:**
   ```bash
   # Check pod status
   {kubectl get pods -l app={cell-id}}

   # Verify health endpoint
   curl -sf http://{host}:{port}/readyz
   ```

4. **Check for data migration conflicts:**
   - If the rolled-back version has a different DB schema, check for backward compatibility
   - If a migration was applied, determine if a reverse migration is needed

5. **Post-rollback:**
   - [ ] Notify stakeholders
   - [ ] Monitor error rates for 15 minutes
   - [ ] Create incident ticket if rollback was due to a production issue

### Rollback Limitations

- {Describe any migrations that cannot be rolled back}
- {Describe any event schema changes that affect consumers}

---

## Scaling Guidelines

### Horizontal Scaling

| Metric Trigger             | Current Setting | Scale Action           |
|----------------------------|-----------------|------------------------|
| CPU > 70% sustained 5 min  | {N replicas}    | Add {M} replicas       |
| Memory > 80%               | {N replicas}    | Add {M} replicas       |
| Request queue depth > {X}  | {N replicas}    | Add {M} replicas       |
| p95 latency > {X}ms        | {N replicas}    | Add {M} replicas       |

### Vertical Scaling

| Resource | Current Limit | Max Recommended | Notes                         |
|----------|---------------|-----------------|-------------------------------|
| CPU      | {X cores}     | {Y cores}       | {Beyond Y, scale horizontally}|
| Memory   | {X Gi}        | {Y Gi}          | {Watch for GC pressure}       |

### Database Scaling

- Connection pool size: {current} / max {recommended}
- Read replicas: {describe read replica strategy if applicable}
- Partitioning: {describe table partitioning if applicable}

---

## Monitoring Dashboards

| Dashboard           | URL                                    | What it Shows              |
|----------------------|----------------------------------------|----------------------------|
| Cell Health          | {Grafana URL}                          | Up/down, restart count     |
| Request Metrics      | {Grafana URL}                          | Latency, throughput, errors|
| Outbox Relay         | {Grafana URL}                          | Relay lag, DLQ count       |
| Resource Utilization | {Grafana URL}                          | CPU, memory, disk, network |

### Key Alerts

| Alert Name                  | Condition                    | Severity | Runbook Section       |
|-----------------------------|------------------------------|----------|-----------------------|
| {CellDown}                  | {healthz fails for > 1 min}  | critical | Issue 1               |
| {HighOutboxLag}             | {lag > 1000 messages}        | warning  | Issue 2               |
| {HighLatency}               | {p95 > 500ms for 5 min}     | warning  | Issue 3               |

---

## Contacts

| Role               | Name / Team          | Contact                            |
|--------------------|----------------------|------------------------------------|
| Cell Owner         | {name}               | {Slack channel or email}           |
| On-call            | {rotation name}      | {PagerDuty or escalation path}     |
| DBA                | {name / team}        | {contact}                          |
| Platform Team      | {name / team}        | {contact}                          |
