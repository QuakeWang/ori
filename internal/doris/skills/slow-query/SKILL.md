---
name: slow-query
description: >
  Analyze slow queries from Doris audit log. Use when the user asks about slow queries, query performance,
  Top SQL, query trends, failed queries, or wants to identify expensive queries.
  Keywords: slow query, TopSQL, query analysis, failed queries, query trends.
---

# Slow Query Analysis

Analyze slow/expensive queries from Doris's built-in audit log table.

## Data Source

Doris stores query audit logs in `__internal_schema.audit_log`. Key columns:

| Column | Description |
|---|---|
| `query_id` | unique query identifier |
| `time` | query start time |
| `user` | query user |
| `db` | target database |
| `query_time` | execution time in ms |
| `scan_bytes` / `scan_rows` | data scanned |
| `return_rows` | rows returned to client |
| `shuffle_send_bytes` | network shuffle data volume |
| `peak_memory_bytes` | peak memory usage |
| `cpu_time_ms` | total CPU time in ms |
| `sql_digest` | SQL fingerprint (hash of normalized SQL) |
| `state` | query state: `EOF` = success, `ERR` = error, `OK` = DDL success |
| `error_code` / `error_message` | error details (when state != EOF) |
| `workload_group` | resource group |
| `stmt` | full SQL text |

## Workflow

### Step 0: Digest Quality Check (always run first)

Some Doris versions do not generate `sql_digest` properly — they fill it with the MD5 of an empty string (`d41d8cd98f00b204e9800998ecf8427e`) or leave it empty. When this happens, `GROUP BY sql_digest` is useless because unrelated SQL statements collapse into one group.

**Always run this probe first** to decide the grouping strategy:

```
doris_sql(sql="SELECT COUNT(*) AS total, COUNT(DISTINCT sql_digest) AS distinct_digests FROM __internal_schema.audit_log WHERE `time` >= DATE_SUB(NOW(), INTERVAL 24 HOUR) AND is_query=1 AND query_time > 1000", max_rows=1)
```

- If `distinct_digests >= 3` → use `GROUP BY sql_digest` (normal path)
- If `distinct_digests < 3` AND `total > distinct_digests * 3` → **digest is degenerate**, use fallback grouping: `GROUP BY LEFT(REGEXP_REPLACE(REPLACE(REPLACE(stmt, '\\n', ' '), '\\t', ' '), '  +', ' '), 200)` instead of `GROUP BY sql_digest`

> **IMPORTANT**: Doris `audit_log.stmt` stores newlines as literal two-character `\n` (backslash + n, `0x5C 0x6E`), NOT as real newline `CHAR(10)`. You MUST use `REPLACE(stmt, '\\n', ' ')` before any regex normalization. Using `REGEXP_REPLACE(stmt, '[\n]+', ' ')` alone will NOT work.

### Step 1: Slow Query Patterns (deduplicated)

**All slow query queries MUST use grouping to deduplicate.** Never query individual rows without grouping — the same SQL pattern may execute thousands of times, and showing raw duplicates is useless.

**Grouping strategy** (determined by Step 0):
- Normal: `GROUP BY sql_digest`
- Fallback: `GROUP BY LEFT(REGEXP_REPLACE(REPLACE(REPLACE(stmt, '\\n', ' '), '\\t', ' '), '  +', ' '), 200)` — when digest is degenerate

Choose one of the following two modes based on user intent. The SQL templates below use `GROUP BY sql_digest`; if Step 0 determined fallback is needed, replace the GROUP BY clause accordingly.

#### Mode A — Impact Ranking (default)

Use when user asks generally about slow queries, slow query situation, or query performance without specifying a count. Ranks patterns by **total cluster impact** (frequency × avg latency).

```
doris_sql(sql="SELECT ANY_VALUE(query_id) AS query_id, ANY_VALUE(user) AS user, ANY_VALUE(db) AS db, COUNT(*) AS exec_count, ROUND(AVG(query_time)) AS avg_ms, MAX(query_time) AS max_ms, ROUND(AVG(scan_bytes)) AS avg_scan, ROUND(AVG(peak_memory_bytes)) AS avg_mem, LEFT(REGEXP_REPLACE(REPLACE(REPLACE(MIN(stmt), '\\\\n', ' '), '\\\\t', ' '), '  +', ' '), 80) AS sql_preview FROM __internal_schema.audit_log WHERE `time` >= DATE_SUB(NOW(), INTERVAL 1 HOUR) AND is_query=1 AND query_time > 1000 GROUP BY sql_digest ORDER BY COUNT(*) * AVG(query_time) DESC LIMIT 15", max_rows=15)
```

If no results in 1 hour, expand to `INTERVAL 24 HOUR`. If still empty, expand to `INTERVAL 7 DAY`.

A pattern appearing 100 times at 2s each (total impact = 200s) is worse than a single 10s query.

#### Mode B — TopN by Latency

Use when user explicitly asks for "N条耗时最高的SQL", "top N slowest queries", or similar requests specifying a count. Ranks patterns by **maximum single-execution latency**.

```
doris_sql(sql="SELECT ANY_VALUE(query_id) AS query_id, ANY_VALUE(user) AS user, ANY_VALUE(db) AS db, COUNT(*) AS exec_count, ROUND(AVG(query_time)) AS avg_ms, MAX(query_time) AS max_ms, ROUND(AVG(scan_bytes)) AS avg_scan, ROUND(AVG(peak_memory_bytes)) AS avg_mem, LEFT(REGEXP_REPLACE(REPLACE(REPLACE(MIN(stmt), '\\\\n', ' '), '\\\\t', ' '), '  +', ' '), 80) AS sql_preview FROM __internal_schema.audit_log WHERE `time` >= DATE_SUB(NOW(), INTERVAL 24 HOUR) AND is_query=1 AND query_time > 1000 GROUP BY sql_digest ORDER BY MAX(query_time) DESC LIMIT 10", max_rows=10)
```

Adjust `LIMIT` to match the number user requested. Adjust the time interval to match the user's request (e.g., `INTERVAL 24 HOUR` for "最近24h"). If no results, expand the time window.

### Step 2: Hourly Trend (optional)

Run when user asks about trends, or when Step 1 shows concentrated slow queries:

```
doris_sql(sql="SELECT DATE_FORMAT(`time`, '%Y-%m-%d %H:00') AS hour_bucket, COUNT(*) AS total_queries, SUM(CASE WHEN query_time > 1000 THEN 1 ELSE 0 END) AS slow_count, ROUND(AVG(query_time)) AS avg_ms, MAX(query_time) AS max_ms, SUM(scan_bytes) AS total_scan_bytes FROM __internal_schema.audit_log WHERE `time` >= DATE_SUB(NOW(), INTERVAL 24 HOUR) AND is_query=1 GROUP BY hour_bucket ORDER BY hour_bucket DESC LIMIT 24", max_rows=24)
```

### Step 3: Failed Query Analysis (optional)

Run when Step 1 shows `state != EOF` or user asks about failed queries:

```
doris_sql(sql="SELECT state, error_code, COUNT(*) AS cnt, LEFT(MIN(stmt), 120) AS sample_sql, LEFT(MIN(error_message), 150) AS sample_error FROM __internal_schema.audit_log WHERE `time` >= DATE_SUB(NOW(), INTERVAL 1 HOUR) AND is_query=1 AND state != 'EOF' GROUP BY state, error_code ORDER BY cnt DESC LIMIT 10", max_rows=10)
```

### Step 4: Drill-down (optional)

For the top 1-2 worst query patterns from Step 1, do a deep analysis:

```
Run explain-analyze on <full_sql_from_step1>
```

This leverages the explain-analyze skill for EXPLAIN and/or profile-level analysis. Do NOT just run bare `EXPLAIN` — use the full skill.

## Analysis Rules

After collecting data, identify the actual problems. Do NOT just list metrics — explain the root cause and impact.

Key diagnostic patterns:
- **High-frequency slow pattern**: Same `sql_digest` > 10 times with avg > 2s → optimize this SQL pattern first (most impactful)
- **Scan-heavy but few results**: `scan_bytes` > 1GB but `return_rows` < 100 → missing filters or index
- **Heavy shuffle**: `shuffle_send_bytes` > `scan_bytes` × 0.5 → consider colocate or bucket optimization
- **Memory pressure**: `peak_memory_bytes` > 2GB → risk of OOM under concurrency
- **Frequent failures**: `state = ERR` count > 10 in 1 hour → investigate error_code patterns
- **CPU amplification**: `cpu_time_ms` >> `query_time` (> 5x) → high parallelism, check resource consumption
- **Trend spike**: Slow count > 3x of adjacent hours → performance regression, check recent changes

## Output Format

**Principle**: Be concise. State each finding ONCE. The entire response should be readable in under 1 minute.

**Structure**:
1. **Diagnosis** — 2-3 sentences: what is the slow query situation, which pattern has the highest impact, why
2. **Evidence table** — ONE table with all columns including `sql_preview` as the last column. Include the SQL preview directly in the table; do NOT create a separate SQL list.

Format data sizes in human-readable units (KB, MB, GB). Format row counts with unit suffixes (K, M, B). Do NOT truncate or abbreviate the `query_id` column; output the full UUID string.

**Example** (target length and density):
```
The dominant slow pattern in the past hour is the custdist query in tpch (85 runs x 3.2s = 272s total impact), scanning 1.8GB each time — a classic large-scan-small-result pattern. The second pattern is an orders-lineitem JOIN scanning 4.2GB but returning fewer than 50 rows, likely missing partition filters.

| # | Query ID | User | Database | Count | Avg | Max | Avg Scan | SQL Preview |
|---|---|---|---|---|---|---|---|---|
| 1 | 9b3d875a1c224abc-bb34750d225bfcc3 | analyst | tpch | 85 | 3.2s | 8.1s | 1.8GB | SELECT c_count, COUNT(*) FROM customer JOIN orders ... |
| 2 | a12c123f8c554ddd-94588a7292739107 | etl_user | tpch | 12 | 8.5s | 14.9s | 4.2GB | SELECT l_orderkey, SUM(l_extendedprice) FROM lineitem ... |

Suggestion: run explain-analyze on pattern #1 for deeper plan and profile analysis.
```

Do NOT:
- Do not repeat the same finding across multiple sections
- Do not add filler paragraphs ("not the main cause", "additional notes", "next steps")
- Do not use more than 2 tables
- Do not generate a separate paragraph for each individual query
- Do not list all audit_log columns in the output
- Do not show queries with latency < 1s (unless the user explicitly asks)
- Do not create a separate "SQL Previews" section — SQL must be in the evidence table
- Do not query individual rows without grouping — always deduplicate
