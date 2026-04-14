# Postmortem: {Incident Title}

<!-- Incident postmortem template — blameless by design.
     Focus on systems and processes, not individuals. -->

## Summary

| Field              | Value                                                    |
|--------------------|----------------------------------------------------------|
| Incident ID        | {INC-NNNN}                                               |
| Date               | {YYYY-MM-DD}                                             |
| Duration           | {e.g. 2h 15m}                                            |
| Severity           | {P1 / P2 / P3 / P4}                                     |
| Status             | {draft / reviewed / complete}                            |
| Author             | {name}                                                   |
| Reviewers          | {name, name}                                             |
| Cell(s) Affected   | {cell-id, cell-id}                                       |

### One-Line Summary

<!-- One sentence describing what happened and its impact. -->

{e.g. "A database connection pool exhaustion in access-core caused 503 errors for all login requests for 2 hours."}

---

## Timeline

<!-- Use UTC timestamps. Be precise. Include detection, escalation, and resolution. -->

| Time (UTC)   | Event                                                         |
|--------------|---------------------------------------------------------------|
| {HH:MM}      | {First occurrence of the issue (e.g. error rate spike)}       |
| {HH:MM}      | {Alert fired: {alert name}}                                   |
| {HH:MM}      | {On-call engineer acknowledged}                               |
| {HH:MM}      | {Initial diagnosis: {what was suspected}}                     |
| {HH:MM}      | {Escalation to {team/person}}                                 |
| {HH:MM}      | {Mitigation applied: {what was done}}                         |
| {HH:MM}      | {Service restored / metrics returned to normal}               |
| {HH:MM}      | {Incident declared resolved}                                  |

### Detection

<!-- How was the incident detected? Alert, customer report, manual observation? -->

{Describe how the incident was first detected and by whom.}

---

## Impact

### User Impact

| Metric               | Value                                                   |
|----------------------|---------------------------------------------------------|
| Users affected       | {number or percentage}                                  |
| Requests failed      | {number or percentage}                                  |
| Error type           | {e.g. 503 Service Unavailable, timeout}                 |
| User-visible symptom | {What users experienced, e.g. "Login page returned error"} |

### Data Impact

| Metric               | Value                                                   |
|----------------------|---------------------------------------------------------|
| Data loss            | {Yes / No — describe if yes}                            |
| Data inconsistency   | {Yes / No — describe if yes}                            |
| Events lost/delayed  | {number, e.g. "~500 events delayed by 30 min"}         |

### Revenue / Business Impact

| Metric               | Value                                                   |
|----------------------|---------------------------------------------------------|
| Revenue impact       | {estimated amount or "none"}                            |
| SLA breach           | {Yes / No — which SLA}                                  |
| Customer escalations | {number}                                                |

---

## Root Cause Analysis

### Root Cause

<!-- Describe the technical root cause. Be specific.
     Use the "5 Whys" technique if helpful. -->

{Detailed description of what caused the incident at a technical level.}

### Contributing Factors

<!-- List factors that made the incident worse or delayed detection/resolution. -->

- {Factor 1: e.g. "Missing alert for connection pool utilization"}
- {Factor 2: e.g. "Runbook did not cover this failure mode"}
- {Factor 3: e.g. "Load test did not simulate sustained peak traffic"}

### 5 Whys

1. **Why did {symptom} happen?** Because {cause 1}.
2. **Why did {cause 1} happen?** Because {cause 2}.
3. **Why did {cause 2} happen?** Because {cause 3}.
4. **Why did {cause 3} happen?** Because {cause 4}.
5. **Why did {cause 4} happen?** Because {root cause}.

---

## What Went Well

<!-- Acknowledge things that worked as expected or better than expected. -->

- {e.g. "Alerting detected the issue within 2 minutes"}
- {e.g. "Rollback procedure worked as documented"}
- {e.g. "Team communication was clear and timely"}

## What Went Poorly

<!-- Acknowledge things that did not work as expected. -->

- {e.g. "Root cause took 45 minutes to identify due to missing metrics"}
- {e.g. "Runbook was outdated and did not cover this scenario"}
- {e.g. "Escalation path was unclear"}

---

## Action Items

<!-- Each action item must have an owner and a due date.
     Categorize as: prevent, detect, mitigate, or process. -->

| ID   | Type     | Action                                          | Owner        | Due Date   | Status   |
|------|----------|-------------------------------------------------|--------------|------------|----------|
| AI-1 | prevent  | {e.g. "Add connection pool size limit to config"} | {name}     | {YYYY-MM-DD} | {open / in-progress / done} |
| AI-2 | detect   | {e.g. "Add alert for pool utilization > 80%"}   | {name}       | {YYYY-MM-DD} | {open}   |
| AI-3 | mitigate | {e.g. "Add circuit breaker for DB connections"}  | {name}       | {YYYY-MM-DD} | {open}   |
| AI-4 | process  | {e.g. "Update runbook with this failure mode"}   | {name}       | {YYYY-MM-DD} | {open}   |

### Action Item Types

- **prevent**: Eliminate the root cause so it cannot recur
- **detect**: Improve monitoring/alerting to catch the issue faster
- **mitigate**: Reduce the blast radius or speed up recovery
- **process**: Improve team processes, documentation, or communication

---

## Lessons Learned

<!-- Key takeaways that the broader team should internalize. -->

1. {Lesson 1: e.g. "Connection pool exhaustion is a common failure mode — every Cell should have pool utilization metrics and alerts."}
2. {Lesson 2: e.g. "Outbox relay lag can mask event delivery failures — monitor both relay lag and consumer lag independently."}
3. {Lesson 3: e.g. "Load testing should simulate sustained peak, not just burst traffic."}

---

## Appendix

### Related Incidents

- {INC-NNNN: Brief description if this is a recurrence or related pattern}

### Metrics / Graphs

<!-- Attach or link to relevant dashboard screenshots or metric queries. -->

- {Link to Grafana dashboard snapshot during the incident}
- {Link to relevant log query}

### References

- {Link to relevant runbook}
- {Link to related ADR if a design decision contributed to the incident}
- {Link to ticket/issue tracker}
