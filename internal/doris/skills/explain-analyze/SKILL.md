---
name: explain-analyze
description: >
  Analyze Doris query execution plan and optionally query profile.
  Use when the user pastes a SQL query asking for optimization, asks why a query is slow,
  wants to understand a query plan, provides a query_id for profile analysis,
  or asks about runtime filters, partition pruning, or join strategies.
---

# Explain Analyze

Analyze a SQL query's execution plan and provide optimization suggestions.

Two modes:
- **Plan mode** (default): Run EXPLAIN VERBOSE on the SQL and analyze the plan.
- **Profile mode**: When the user provides a `query_id`, analyze the real execution profile.

When the user provides a `query_id` and asks for slow query analysis, **always use both modes** — run EXPLAIN on the SQL and collect/retrieve the profile. Do NOT only use one.

## Getting the SQL from a query_id

When the user provides only a `query_id`, first retrieve the original SQL:

```
doris_sql(sql="SELECT stmt FROM __internal_schema.audit_log WHERE query_id = '<query_id>' LIMIT 1")
```

## Plan Mode Workflow

### Step 1: Run EXPLAIN VERBOSE

```
doris_sql(sql="EXPLAIN VERBOSE <user_sql>")
```

Use `EXPLAIN VERBOSE` (not plain EXPLAIN) to get runtime filter details, tuple ids, and cardinality information.

### Step 2: Analyze the plan

Examine the EXPLAIN output for these patterns:

#### Scan Node Patterns

| Pattern | Problem | Suggestion |
|---|---|---|
| `partitions=N/N` (N>1, all selected) | No partition pruning | Add partition column filter in WHERE |
| `PREAGGREGATION: OFF` | Pre-aggregation disabled | Check if query columns match key columns; see the OFF reason in output |
| `cardinality=0` or `cardinality=-1` | Missing or stale statistics | Run `ANALYZE TABLE db.table` to collect statistics |
| Large `tablets=` ratio (most tablets selected) | Scanning too many tablets | Add more selective filters or distribution key filters |
| No `runtime filters:` line on ScanNode | No runtime filter applied | Check if join conditions support RF generation |
| `TOPN OPT` absent for ORDER BY + LIMIT | TopN pushdown not triggered | Ensure LIMIT is present and sort columns are in key prefix |

#### Join Node Patterns

| Pattern | Problem | Suggestion |
|---|---|---|
| `HASH JOIN (BROADCAST)` with large right child cardinality | Large table broadcast, memory pressure | Switch to SHUFFLE join (via hint or adjust table sizes) |
| `HASH JOIN (SHUFFLE)` on colocate-eligible tables | Missing colocate optimization | Set tables to same colocate group |
| `HASH JOIN (BUCKET_SHUFFLE)` degraded to `SHUFFLE` | Bucket shuffle not applicable | Check distribution key alignment between joined tables |
| No runtime filter generated (`<-` absent on Join node) | RF not generated for this join | Check join type and session variable `runtime_filter_type` |

#### Runtime Filter Patterns

| Pattern | Problem | Suggestion |
|---|---|---|
| RF type `bloom` but ndv is very small (< 1024) | Bloom filter overhead for small cardinality | Consider enabling IN filter (`set runtime_filter_type = IN_OR_BLOOM`) |
| RF `actualSize` much larger than `expectSize` | Filter size capped by session limit | Increase `runtime_bloom_filter_max_size` if memory allows |
| RF generated but not applied to any ScanNode | RF target missing | Check if scan node is in a different fragment or filtered out |

#### Exchange / Distribution Patterns

| Pattern | Problem | Suggestion |
|---|---|---|
| `EXCHANGE` with BROADCAST distribution for large data | Network bottleneck | Consider SHUFFLE or colocate join |
| Multiple `EXCHANGE` nodes between same fragments | Redundant data shuffling | Review query structure, consider CTE or subquery refactoring |

#### Other Patterns

| Pattern | Problem | Suggestion |
|---|---|---|
| `SORT` node without TOPN optimization for LIMIT queries | Full sort before limit | Ensure LIMIT is directly on the ORDER BY query |
| `AGGREGATE` without streaming pre-aggregation | Full aggregation in memory | Check if aggregate columns match key model |

### Step 3: Check statistics (optional)

If Step 2 reveals `cardinality=0`, `cardinality=-1`, or suspiciously inaccurate cardinality estimates, check table statistics:

```
doris_sql(sql="SHOW TABLE STATS db.table")
doris_sql(sql="SHOW COLUMN STATS db.table")
```

If statistics are missing or stale, suggest running `ANALYZE TABLE db.table`.

### Step 4: Check related metadata (optional)

If the plan references specific tables with issues, optionally check:

```
doris_sql(sql="SHOW CREATE TABLE db.table")
doris_sql(sql="SHOW PARTITIONS FROM db.table", columns="PartitionName,Buckets,DataSize,RowCount")
```

## Profile Mode Workflow

When the user provides a `query_id` or asks to analyze a specific SQL's runtime performance.

### Step P1: Get or collect query profile

**Option A — query_id provided**: Use `doris.profile` to retrieve the Doris-native merged view first:

```
doris_profile(query_id="<query_id>", view="merged")
```

If `doris.profile` returns an error (profile expired or not found), fallback to Option B.

**Option B — profile not found, or user asks you to collect**: Proactively collect a fresh profile.
Do NOT ask the user to re-run. Execute it yourself using `doris.session` to keep SET and query on the same connection:

```
doris_session(setup_sqls=["SET enable_profile = true"], sql="<the original SQL>")
```

Then retrieve the profile for the latest execution:

```
doris_sql(sql="SHOW QUERY PROFILE '/'")
```

Pick the matching `query_id` from the list and retrieve the merged view first:

```
doris_profile(query_id="<new_query_id>", view="merged")
```

> **IMPORTANT**: When the user says "collect profile", "run the profile", or "execute it yourself", you MUST execute the SQL yourself with `enable_profile=true`. Do NOT deflect back to the user.

### Step P2: Summary layer — overall assessment

Check `Total` and `Task State` from the Summary section:
- `Task State = ERR` or `CANCELLED` → focus on error cause, not performance
- `Total` < 100ms → query is already fast, only optimize if user insists

### Step P3: Execution Summary layer — locate FE-side bottleneck

The Execution Summary breaks the total time into FE phases:

| Phase | Key Metric | If dominant |
|---|---|---|
| Planning | `Plan Time` (includes `Nereids Analysis/Rewrite/Optimize Time`) | Complex SQL or missing stats causing long optimization; simplify SQL or use leading hint |
| Scheduling | `Schedule Time` (includes `Fragment Assign/Serialize/RPC Phase1&2`) | Too many fragments or BE nodes overloaded |
| Execution | `Wait and Fetch Result Time` | BE-side execution is the bottleneck → proceed to Step P4 |

Also note:
- `Workload Group`: check resource group limits (Max Concurrency, Memory Watermark)
- `Scan Thread Num` / `Max Remote Scan Thread Num`: scan parallelism configuration

### Step P4: Operator-level deep analysis

When `merged` view identifies the hotspot fragment or operator family, retrieve the detail view for drilldown:

```
doris_profile(query_id="<query_id>", view="detail")
```

Profile Detail is organized as `Fragment → Pipeline → Operator`. Each operator has `CommonCounters` and `CustomCounters`.

**Step P4.1 — Build the operator timeline**: Extract **every** operator's `ExecTime` from the profile text and sort by descending time. Present this as an **Operator Hotspot Table**:

```
| Rank | Operator | Fragment | ExecTime | Rows In | Rows Out | Memory | Bottleneck? |
|------|----------|----------|----------|---------|----------|--------|-------------|
| 1    | VOLAP_SCAN (lineitem) | F1 | 12.5s | - | 300M | 1.2GB | ★ |
| 2    | VHASH_JOIN (BROADCAST) | F1 | 8.3s | 300M+10M | 280M | 2.1GB | ★ |
| 3    | VAGGREGATE (update) | F0 | 6.2s | 280M | 50 | 800MB | |
```

Mark operators consuming >30% of total execution time as bottlenecks (★).

**Step P4.2 — Drill into bottleneck operators**: For each ★ operator, examine its specific counters:

**Scan operators (VOLAP_SCAN)**:
- `RowsRead` vs `RowsProduced`: ratio > 10x = poor predicate pushdown
- `ScanBytes`: > 1GB = full table scan or missing partition pruning
- `NumScanners = 1`: under-parallelized scan
- `RuntimeFilterRows`: if 0 but RF was generated, RF not effective
- `SegmentRead` vs `TotalSegmentNum`: high ratio = too many segments (compaction issue)

**Join operators (VHASH_JOIN)**:
- `ProbeRows` vs `RowsProduced`: high ratio = join selectivity issue
- `BuildRows`: if very large for BROADCAST join = should switch to SHUFFLE
- `RuntimeFilterBuildTime`: if significant = RF construction overhead

**Aggregate operators (VAGGREGATE)**:
- `HashTableSize`: if very large = high cardinality aggregation
- `GetNewBlockForWriteTime` / `GetResultTime`: memory allocation overhead
- `PartialUpdateRows`: partial aggregation effectiveness

**Sort operators (VSORT / VTOP-N)**:
- If VTOP-N: `Rows` >> LIMIT value = TopN not pushed down effectively
- If spilling occurs: check `SpillWriteBytes` / `SpillReadBytes`

**Exchange operators (VEXCHANGE)**:
- `WaitForDependencyTime`: if >> `ExecTime`, the operator is stalled waiting for upstream
- `NetworkTime`: network transfer overhead
- `PeakMemoryUsage`: data buffering pressure

**Step P4.3 — Explain the causal chain**: Connect the bottleneck operators into a data flow narrative, e.g.:
"lineitem full scan 300M rows (12.5s) → BROADCAST JOIN with part 10M rows (8.3s, should use SHUFFLE) → COUNT(DISTINCT) aggregation maintains 280M dedup states (6.2s)"

## Error Handling

- If a query fails due to insufficient privileges, note it as `info: insufficient privilege, skipped` and continue with remaining steps.
- If EXPLAIN fails (e.g., table not found, syntax error), report the error directly to the user without further analysis.

## Output Format

**Principle**: Be concise. State each finding ONCE. The entire response should be readable in under 2 minutes.

**Structure** (all modes — output ONE unified analysis, do NOT split into separate Explain/Profile sections):
1. **Original SQL** — show the complete SQL in a code block so the user can immediately see which query is being analyzed
2. **One-line conclusion** — the root cause, in one sentence
3. **Causal chain** — 2-4 lines showing data flow: `Scan X rows → Join → Agg → result`
4. **Operator Hotspot Table** — the top operators by ExecTime (from Step P4.1), if profile is available
5. **Key EXPLAIN findings** — important plan-level observations (partition pruning, RF, join strategy) that weren't already covered
6. **Suggestions** — 1-3 actionable items, ordered by priority

**Example** (this is the target length and density):

~~~
**原始 SQL**
```sql
SELECT p.p_size, COUNT(DISTINCT l.l_orderkey) AS order_count, SUM(l.l_quantity) AS total_qty
FROM part p JOIN lineitem l ON p.p_partkey = l.l_partkey
GROUP BY p.p_size ORDER BY total_qty DESC LIMIT 15;
```

**结论**：lineitem 全表扫描 3 亿行 + BROADCAST JOIN + COUNT(DISTINCT) 去重聚合，导致总耗时 40.5s。

**因果链**：lineitem 全表扫描 300M rows / 5.7GB → BROADCAST JOIN with part 10M rows → COUNT(DISTINCT l_orderkey) 维护去重状态 → VTOP-N 返回 15 行。前三步各占 12s/8s/6s，构成瓶颈。

**算子耗时分析**

| # | Operator | ExecTime | Rows | Memory | 分析 |
|---|---|---|---|---|---|
| 1 | VOLAP_SCAN (lineitem) | 12.5s ★ | 300M | 1.2GB | 全表扫描 96/96 tablets，无过滤条件 |
| 2 | VHASH_JOIN (BROADCAST) | 8.3s ★ | 300M+10M→280M | 2.1GB | part 表 10M 行 broadcast，内存压力大 |
| 3 | VAGGREGATE | 6.2s | 280M→50 | 800MB | COUNT(DISTINCT) 需维护去重状态 |

**EXPLAIN 补充**：cardinality 未知（statistics missing），PREAGGREGATION ON，无 runtime filter 生效。

**建议**：
1. 增加 lineitem 过滤条件减少扫描量
2. 运行 `ANALYZE TABLE tpch.lineitem` 和 `ANALYZE TABLE tpch.part` 收集统计信息
3. 评估 COUNT(DISTINCT) 是否可用近似算法（APPROX_COUNT_DISTINCT）替代
~~~

Do NOT:
- Do not repeat the same finding across multiple sections
- Do not add filler paragraphs ("not the main cause", "additional notes", "next steps")
- Do not use more than 2 tables
- Do not split Explain and Profile conclusions into separate sections causing duplication — merge into one analysis
- Do not skip showing the full original SQL
