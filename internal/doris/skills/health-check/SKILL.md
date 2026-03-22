---
name: health-check
description: >
  Doris cluster health check and data overview. Use when the user asks to inspect cluster status,
  check node health, review disk usage, perform a general cluster inspection, list databases,
  show large or biggest tables, check table sizes, or get an overview of data distribution.
  IMPORTANT: Execute all steps directly in sequence.
blocked_sql:
  - "SHOW\\s+CREATE\\s+TABLE"
  - "SHOW\\s+PARTITIONS"
  - "SHOW\\s+PROC\\s+'/STATISTIC'"
---

# Doris Health Check

Cluster-level health scan and data overview.

For deep single-table audit (DDL, partition, bucket, distribution design), use the `schema-audit` skill instead.

Execute all steps below directly in sequence.

## Step 1: Run cluster checks

Run EXACTLY these 2 SQL queries. Do NOT run any other queries in this step.

**Query 1:** `doris_sql(sql="SHOW FRONTENDS")`
- Check: any row where `Alive != true` → finding (critical)

**Query 2:** `doris_sql(sql="SHOW BACKENDS")`
- Check: any row where `Alive != true` → finding (critical)
- Check (storage-coupled only, skip if column is empty or missing): `MaxDiskUsedPct > 80` → finding (warn); `> 90` → finding (critical)
- Check (storage-coupled only, skip if column is empty or missing): `CompactionScore > 150` → finding (warn)

## Step 2: Run data overview

Run EXACTLY these 2 SQL queries. Do NOT run any other queries in this step.

**Query 3 — Database summary:**

```sql
SELECT
  t.TABLE_SCHEMA AS db,
  COUNT(*) AS tables,
  SUM(t.DATA_LENGTH) AS total_bytes,
  SUM(t.TABLE_ROWS) AS total_rows
FROM information_schema.tables t
WHERE t.TABLE_TYPE = 'BASE TABLE'
  AND t.TABLE_SCHEMA NOT IN ('information_schema','mysql','__internal_schema')
GROUP BY t.TABLE_SCHEMA
ORDER BY total_bytes DESC
```

**Query 4 — Top 20 largest tables:**

```sql
SELECT
  t.TABLE_SCHEMA AS db,
  t.TABLE_NAME AS tbl,
  t.DATA_LENGTH AS data_bytes,
  t.TABLE_ROWS AS rows
FROM information_schema.tables t
WHERE t.TABLE_TYPE = 'BASE TABLE'
  AND t.TABLE_SCHEMA NOT IN ('information_schema','mysql','__internal_schema')
ORDER BY t.DATA_LENGTH DESC
LIMIT 20
```

## Step 3: Run schema batch scan

Run EXACTLY this 1 SQL query. Do NOT run any other queries in this step.

**Query 5 — Empty partition scan:**

```sql
SELECT
  t.TABLE_SCHEMA AS db,
  t.TABLE_NAME AS tbl,
  COUNT(p.PARTITION_NAME) AS partitions,
  SUM(CASE WHEN p.DATA_LENGTH = 0 AND (p.TABLE_ROWS IS NULL OR p.TABLE_ROWS = 0) THEN 1 ELSE 0 END) AS empty_parts,
  SUM(p.DATA_LENGTH) AS total_bytes
FROM information_schema.tables t
LEFT JOIN information_schema.partitions p
  ON t.TABLE_SCHEMA = p.TABLE_SCHEMA AND t.TABLE_NAME = p.TABLE_NAME
WHERE t.TABLE_TYPE = 'BASE TABLE'
  AND (t.ENGINE = 'Doris' OR t.ENGINE = 'OLAP')
  AND t.TABLE_SCHEMA NOT IN ('information_schema','mysql','__internal_schema')
GROUP BY t.TABLE_SCHEMA, t.TABLE_NAME
ORDER BY empty_parts DESC
LIMIT 200
```

- Check: `empty_parts / partitions > 0.3` → finding (warn); `> 0.6` → finding (critical)
- Check: all tables in a DB have `TABLE_ROWS = 0` → finding (info): empty database

## Step 4: Output the report

After running the 5 queries above, output the report. Do NOT run any additional queries (no SHOW CREATE TABLE, no SHOW PARTITIONS, no SHOW PROC '/statistic').

**Principle**: Be concise. State each finding ONCE. The entire response should be readable in under 1 minute.

**Structure**:
1. **One-line assessment** — overall status (Healthy / Warning / Critical) with key numbers
2. **Findings table** — ONE table listing all triggered findings. List ALL without truncation.
3. **Database overview** — ONE compact table (top databases by size)
4. If no findings: **Cluster is healthy, no issues found.**

Format data sizes in human-readable units (KB, MB, GB). Format row counts with unit suffixes (K, M, B).

**Example** (target length and density):
```
Cluster fe.example:9030 is running normally. 3 FE + 5 BE all alive. Max disk usage 72.1%, no alert triggered. 2 issues found.

| # | Severity | Object | Issue | Current Value |
|---|---|---|---|---|
| 1 | warn | BE:192.168.1.1 | Disk usage high | 82.3% |
| 2 | warn | mydb.events | High empty partition ratio | 85/120 (70.8%) |

| Database | Tables | Data Size | Rows |
|---|---|---|---|
| prod_db | 8 | 8.2 GB | 86M |
```

Do NOT:
- Do not repeat the same finding in multiple places
- Do not add "Next Steps" / "Suggestions" / "Optimization" sections (health-check only reports facts)
- Do not use more than 2 tables
- Do not run SHOW CREATE TABLE or SHOW PARTITIONS (those belong to schema-audit)
- Do not add optimization advice (unless the user explicitly asks)

