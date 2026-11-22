# Architecture & Performance

**[English](architecture-and-performance.md) · [简体中文](zh-CN/architecture-and-performance.md) · [Documentation Hub](README.md) · [Benchmarks](benchmarks.md) · [Privacy](privacy.md)**

Rolling Memory is a local-first memory service for AI agents. It stores what happened, builds a retrieval view over it, and returns evidence to the agent. This page describes the architecture at a product level and the latency you should expect from a local vault. Wire formats, storage schemas and scoring operators are intentionally out of scope.

---

## 1. At a glance

| Stage | What happens | What it means for you |
|---|---|---|
| **Ingest** | The unit is written to the local vault and recorded as pending. | The write is durable immediately. |
| **Publish** | A background builder folds pending changes into the next published generation. | The generation is immutable and can be served without locks. |
| **Recall** | A query reads the current published generation and returns ranked evidence. | Retrieval never waits for an ingest and never builds on the query path. |

The important consequence: **recall is asynchronous with respect to ingest**. A write becomes searchable after the next publish. Deletes and updates follow the same rule. This is a deliberate trade: predictable, low-latency recall during large imports, at the cost of a short visibility delay.

```text
ingest ──▶ local vault + pending changes
                     │
                     ▼
          background builder (batched)
                     │
                     ▼
          published generation (immutable)
                     │
                     ▼
                  recall
```

## 2. The three memory layers

All three layers live in the same local vault and share one retrieval surface:

| Layer | Typical content | Role |
|---|---|---|
| **Background** | Mounted files, documents, long-lived reference material | Stable context that should not be re-read on every turn |
| **Episodic** | Conversation turns, events, decisions, timestamps | What happened, in order |
| **Noetic** | Agent recollections with cited evidence | What the agent concluded, and which units support it |

The layers are labels on the same unit contract, not three separate databases. A recollection can cite units from the other layers, and retrieval returns the evidence rather than a synthesized answer.

## 3. Published generations

The canonical vault is the source of truth. The retrieval view is derived from it:

1. Ingest appends or updates a unit and records the change.
2. A background builder takes the pending changes, applies them to the current generation, and publishes the next generation.
3. A published generation contains its own retrieval index and metadata view. It is immutable.
4. Recall opens the current generation and serves it. Queries already in flight finish on the generation they started with; new queries switch to the next generation when it becomes current.

Because the query path only reads an immutable generation, it does not apply pending writes, does not compete with the builder for the vault, and does not rebuild the index while a user is waiting. The builder can run in a separate process from the recall service.

## 4. Performance

### 4.1 Reference setup

The readings below are local engineering measurements, not leaderboard scores:

- Apple M4 Pro (14-core), macOS, local plaintext vault.
- ATM-Bench SGM corpus, first 5,000 units.
- 20 official questions, top-10 results.
- Warmed queries. The first query that sees a new generation pays a one-time open (see §4.3).

### 4.2 Read latency under an ingest backlog

| Unpublished writes | p50 | p99 |
|---:|---:|---:|
| 0 (warmed) | 14.6 ms | ~23–25 ms |
| 200 | 11.5 ms | 23.4 ms |
| 2,000 | 13.6 ms | 25.2 ms |
| 5,000 | 14.1 ms | 25.0 ms |

Pending writes do not change the read path. The small differences are measurement noise. Before the asynchronous read model, a 1,500-write backlog could push p99 into the seconds, and a larger backlog could force readers onto an older view.

### 4.3 Publish latency

| Corpus | Pending writes in one batch | Publish time | Full rebuild (first build or fallback) |
|---:|---:|---:|---:|
| 2,000 | 64 | ~0.5 s | 1.6 s |
| 5,000 | 96 | 1.1–1.4 s | 4.3 s |
| 10,522 | 128 | ~3.7 s | 14.1 s |

Publishing runs in the background on a fixed cadence (60 seconds by default; `daemon.snapshot_interval`, 10s–1h) and only when changes are pending. The table's batch sizes are the reference batches used for measurement; in production a batch is whatever accumulated during the interval. At the 10k scale, a batch normally becomes searchable a few seconds after it is published; at 5k, about a second. A shorter interval reduces visibility delay; a longer one reduces background work. The first build of a vault, or a fallback after an incompatible change, is a full rebuild and takes longer.

**Why a short delay is acceptable.** Daily harness work rarely needs immediate visibility. The current turn — the file just read, the command just run, the message just exchanged — is already in the model's context. Memory retrieval is for older material that fell out of context. A 60-second publish cadence therefore trades almost nothing in practice for stable recall during long sessions and large imports.

### 4.4 The first query after a publish

The first query that sees a new generation opens that generation's index and metadata view. At 10k units this is typically a few hundred milliseconds or less; subsequent queries fall back into the ranges above. If a service needs to avoid even that one-time cost, it can warm the new generation after publication.

### 4.5 What these numbers are not

These are latency measurements, not retrieval-quality scores. Quality is evaluated separately with the frozen protocol in [benchmarks.md](benchmarks.md). Latency depends on hardware, corpus size, query length, `top_k`, and storage performance; treat the table as an order-of-magnitude reference, not a service-level guarantee.

## 5. Operating characteristics

- **Durability is immediate; visibility is eventual.** An acknowledged write is in the vault, but it is not searchable until the next publish.
- **Publishing is paced.** The builder publishes on a fixed cadence (60s by default, configurable) and only when changes are pending; a burst is folded into one generation instead of one build per write.
- **Deletes and updates follow the same rule.** They take effect at the next publish.
- **In-flight queries are stable.** They stay on the generation they started with.
- **No network and no LLM in the retrieval path.** The local arm decodes the query, scores it against the published view, and returns evidence. The harness's model reads that evidence and decides what to do next.
- **The vault is the source of truth.** A published generation is derived and can always be rebuilt from the vault.

## 6. Why this design

- **Predictable recall.** A large import does not make someone else's query slower.
- **Process isolation.** The builder and the recall service have different resource profiles; they can run apart.
- **Reproducibility.** An immutable generation is a stable view for evaluation, replay, and incident analysis.
- **Honest semantics.** The engine returns evidence; it does not hide an LLM extraction or summarization step behind a similarity score.

## 7. Further reading

- **[benchmarks.md](benchmarks.md)** — frozen evaluation protocol, corpus pins, and raw-result recomputation.
- **[privacy.md](privacy.md)** — what stays local, vault defense-in-depth, and the application-based cloud preview.
- **[background-vs-grep.md](background-vs-grep.md)** — when to use recall and background mounts instead of `grep`.
- **[faq.md](faq.md)** — differences from vector RAG, LLM extraction services, and harness-built-in memory.
