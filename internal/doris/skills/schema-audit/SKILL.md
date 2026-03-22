---
name: schema-audit
description: >
  Audit Doris table schema design — DDL, partition, bucket, and distribution analysis.
  Use when the user asks to audit table DDL, check partition/bucket design, review table structure,
  inspect dynamic partition, check bucket settings, analyze bucket size, or perform schema checks.
  Supports both single-table audit and full-database scan.
  Keywords: bucket design, bucket analysis, partition design, distribution key.
max_steps: 80
---

# Schema Audit

Audit Doris table schema design — DDL, partition, bucket, and distribution analysis.

Two modes:
- **Single-table mode**: audit one specific `database.table` in depth.
- **Scan mode**: audit all tables across the cluster for bucket/partition issues.

Pick mode based on user request. If user says "all tables" / "full database" / "entire cluster", use scan mode.

---

## Mode A: Single-Table Audit

User should specify `database.table`. If not provided, ask which table to audit.

### Step 1: Get Table DDL

```
doris_sql(sql="SHOW CREATE TABLE db.table_name", database="db")
```

From the DDL, extract:
- Key type (DUPLICATE / UNIQUE / AGGREGATE)
- Distribution type (HASH / RANDOM) and columns
- BUCKETS count (AUTO or fixed)
- Partition strategy

### Step 2: Get Partition Details

```
doris_sql(sql="SHOW PARTITIONS FROM db.table_name", database="db", columns="PartitionName,Buckets,DataSize,RowCount,Range")
```

### Step 3: Get Index Info

```
doris_sql(sql="SHOW INDEX FROM db.table_name", database="db")
```

### Step 4: Get Dynamic Partition Properties

```
doris_sql(sql="SELECT property_name, property_value FROM information_schema.table_properties WHERE table_schema = 'db' AND table_name = 'table_name' AND property_name LIKE 'dynamic_partition.%'", database="information_schema")
```

Key properties: `dynamic_partition.enable`, `dynamic_partition.start`, `dynamic_partition.end`, `dynamic_partition.time_unit`, `dynamic_partition.prefix`, `dynamic_partition.buckets`.

### Step 5: Apply Checks (see Rules section below)

### Step 6: Output

Report focuses on data findings only. No optimization advice unless user explicitly asks.

The findings table has EXACTLY 4 columns: #, Severity, Issue, Current Status. No other columns.

```
## Schema Audit: db.table_name

**Model**: UNIQUE KEY | **Distribution**: HASH(col) BUCKETS AUTO | **Partition**: RANGE(dt) daily
**Dynamic Partition**: enabled, start=-30, end=3, time_unit=DAY

### Findings

| # | Severity | Issue | Current Status |
|---|---|---|---|
| 1 | warn | High empty partition ratio | 85/120 (70.8%) |
| 2 | warn | Dynamic partition window too wide | window=33 days, empty ratio=70.8% |
| 3 | info | Avg tablet size below recommended | avg 230MB (recommended 1-10GB) |

### Partition Details (Top 5 by size)

| Partition | Buckets | Data Size | Rows | Status |
|---|---|---|---|---|
| p20260101 | 16 | 1.2GB | 5M | OK |

If no issues: **Table schema design is sound, no issues found.**
```

---

## Mode B: Full-Database Scan

When user asks to audit all tables / full database.

### Scan Step 1: Get cluster info and all table sizes

First, get the number of alive BE nodes (needed for bucket evaluation):

```
doris_sql(sql="SELECT COUNT(*) AS be_count FROM information_schema.backends WHERE Alive='true'", database="information_schema")
```

Then get all table sizes, **excluding system schemas and materialized views**:

```
doris_sql(sql="SELECT TABLE_SCHEMA AS db, TABLE_NAME AS tbl, TABLE_ROWS AS row_count, DATA_LENGTH AS data_bytes FROM information_schema.tables WHERE TABLE_TYPE = 'BASE TABLE' AND TABLE_SCHEMA NOT IN ('information_schema','__internal_schema','mysql') AND TABLE_NAME NOT IN (SELECT mvname FROM information_schema.materialized_views) ORDER BY DATA_LENGTH DESC", max_rows=500)
```

### Scan Step 2: Get partition details for ALL non-empty tables

For EVERY table with row_count > 0 or data_bytes > 0, run:

```
doris_sql(sql="SHOW PARTITIONS FROM db.tbl", database="db", columns="PartitionName,Buckets,DataSize,RowCount")
```

From partitions, compute for each table:
- `total_tablets` = SUM(Buckets) across all partitions
- `avg_tablet_size` = partitionDataSize / buckets for each non-empty partition
- `empty_partition_ratio` = empty partitions / total partitions

Apply tablet size rules (SA-B001~B006) and partition rules (SA-E001, SA-E002) to flag issues.

**Batch efficiency**: process multiple tables per step when possible (call multiple doris_sql in one step).

### Scan Step 3: Get DDL only for flagged tables

Only for tables that triggered issues in Step 2, run:

```
doris_sql(sql="SHOW CREATE TABLE db.tbl", database="db")
```

Apply DDL rules (SA-DDL1, SA-DDL2) and dynamic partition rules (SA-D004).

Skip DDL for tables that passed all Step 2 checks.

### Scan Step 4: Produce summary report

**CRITICAL — FULL SCAN RULES (MUST FOLLOW):**
1. You MUST process ALL qualifying tables. Do NOT stop at 5, 10, or any subset.
2. Do NOT ask the user "should I continue?". Just keep going until all qualifying tables are done.
3. Do NOT output partial results. Only output the final report AFTER all qualifying tables are analyzed.
4. Keep iterating on every Continue prompt until all qualifying tables are processed.
5. Then output ONE final complete report.

Output format:

```
## Bucket Audit Scan: All Databases

**Tables scanned**: 45 | **Issues found**: 8

### Findings

| # | Severity | Database | Table | Issue | Current Status |
|---|---|---|---|---|---|
| 1 | critical | mydb | users | UNIQUE KEY + RANDOM distribution | UNIQUE KEY with RANDOM dist |
| 2 | warn | mydb | orders | Avg tablet size too large | avg 20GB across 2 tablets (recommended 1-10GB) |
| 3 | info | mydb | logs | Avg tablet size below 1GB | avg 230MB across 16 tablets |

### Tables Passed (no issues)

All other scanned tables have reasonable bucket settings.
```

---

## Rules Reference

### DDL & Distribution (from DDL)

| Rule | What to check | Trigger | Severity |
|---|---|---|---|
| SA-DDL1 | UNIQUE KEY + RANDOM distribution | DDL match | critical |
| SA-DDL2 | HASH dist columns not in key columns | distKey not subset of keyColumns | critical (UNIQUE) / warn (AGG) |

### Tablet Size (primary bucket evaluation — from SHOW PARTITIONS)

Doris official best practice (Size Principle): each tablet should be **1-10GB**.
Calculation: `avg_tablet_size = partitionDataSize / buckets` (DataSize from SHOW PARTITIONS is already compressed physical size).

| Rule | What to check | Trigger | Severity |
|---|---|---|---|
| SA-B001 | Avg tablet size too small (over-bucketed) | avg_tablet_size < 100MB AND partition data > 0 | critical |
| SA-B002 | Avg tablet size below recommended | 100MB <= avg_tablet_size < 1GB | warn |
| SA-B003 | Avg tablet size too large (under-bucketed) | avg_tablet_size > 10GB | warn |
| SA-B004 | Avg tablet size severely too large | avg_tablet_size > 50GB | critical |
| SA-B005 | Buckets fewer than BE count | buckets < be_count AND partition data > 1GB | info |
| SA-B006 | AUTO bucket jumps | Adjacent partition bucket change > 50% | info/warn |

### Partitions

| Rule | What to check | Trigger | Severity |
|---|---|---|---|
| SA-E001 | High empty partition ratio | > 30% warn, > 60% critical | warn/critical |
| SA-E002 | Trailing empty partition tail | >= 7 consecutive empty at tail | warn |

### Dynamic Partitions

| Rule | What to check | Trigger | Severity |
|---|---|---|---|
| SA-D004 | Wide window causing empty partitions | dynamic_partition.enable=true AND empty ratio >= 60% | warn |
| SA-D004 | Overly wide window span | windowSpan (end - start) > 32 warn, > 64 critical | warn/critical |

---

Do NOT run cluster-level health checks (those belong to `health-check` skill).

Do NOT add optimization advice unless user explicitly asks.

Do NOT:
- Do not repeat the same finding in multiple places
- Do not add "Suggestions" / "Optimization" / "Next Steps" sections (unless the user explicitly asks)
- Do not use more than 3 tables (findings + partition details is sufficient)
- Do not promote other skills at the end of the report (let the user decide)

