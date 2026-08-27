# Indexing performance — measured 2026-08-26

[`LATENCY_BASELINE.md`](LATENCY_BASELINE.md) priced the *prompt* path and found it
already thin: retrieval CPU is ~0.44 ms of a ~1,450 ms turn, and the proxy money path
went 228 → 136 ms with the auth cache. It also named, twice, what it had **not**
measured — "the ONNX embedder call ... or the SQLite vector search", and nothing at all
about building the index.

That is where the time actually was. This document is that measurement and the two
changes it justified.

Same standing rule as the latency work: **a finding that fails to reproduce gets struck
rather than fixed on faith.** One candidate below was measured and struck.

**Machine:** linux/amd64, 13th Gen Intel Core i5-1340P (12 cores), Go 1.25.12.
**Corpus:** a 378-file / 3,305-chunk tree of this repository's own `.go`/`.md`/`.ts`
sources, indexed by the real daemon binary against the real BGE ONNX helper.

## The headline

| Scenario | Before | After | |
|---|---:|---:|---|
| First index of a tree (cold) | 2m15s | 2m28s | unchanged — noise |
| **Re-index, nothing changed** | **2m15s** | **0.86s** | **157×** |
| **Re-index, one file added** | **2m15s** | **0.74s** | **~180×** |
| Lexical `Upsert`, 4,000 chunks | 7.63s | 0.36s | **21×** |
| Lexical `DeleteByFilePath` (per applied edit) | 4.2 ms | 0.57 ms | **~7×** |

The cold path is deliberately untouched: embedding 3,305 chunks costs what it costs.
Everything here is about not paying it twice.

---

## Finding 1 — the lexical store's writes were quadratic

`code_chunks_fts` is an FTS5 virtual table whose `chunk_id` and `file_path` columns are
declared `UNINDEXED`. That keyword means what it says: the column is **stored but carries
no b-tree**. So both write paths —

```sql
DELETE FROM code_chunks_fts WHERE chunk_id  = ?   -- Upsert, once per chunk
DELETE FROM code_chunks_fts WHERE file_path = ?   -- DeleteByFilePath
```

— could not seek. SQLite scanned every row of the table and decoded each one, and each
row carries a full chunk of source text.

Measured before the fix, batching at the shipped `indexEmbedBatchSize` of 40:

| chunks | total | per chunk |
|---:|---:|---:|
| 500 | 222 ms | 443 µs |
| 1,000 | 611 ms | 611 µs |
| 2,000 | 1.60 s | 801 µs |
| 4,000 | 7.63 s | **1,906 µs** |

**Per-chunk cost rising with `n` is the signature.** 8× the chunks cost 34× the time. The
vector store over the identical corpus was flat at ~92 µs/chunk, so the lexical half was
~20× the cost of the semantic half and pulling further away with every chunk added.

It was worse than a slow index build, because `DeleteByFilePath` is on the **interactive**
path: [`reindexFile`](../daemon/reindex.go) calls it on every applied edit, synchronously,
inside the per-workspace apply lock. Every edit a user accepted paid a full scan of the
lexical index — and paid more of it the longer they had been working.

### The fix

A companion `chunk_index(chunk_id PRIMARY KEY, file_path, fts_rowid)` table with an index
on `file_path`. Deletion goes by **rowid**, which FTS5 does support efficiently, and the
FTS table keeps owning search while the companion owns only identity. See
[`daemon/lexicalstore.go`](../daemon/lexicalstore.go).

After, same corpus and batching:

| chunks | total | per chunk |
|---:|---:|---:|
| 500 | 53 ms | 107 µs |
| 1,000 | 136 ms | 136 µs |
| 2,000 | 236 ms | 118 µs |
| 4,000 | **361 ms** | **90 µs** |

Flat, and level with the vector store's 92 µs. The quadratic term is gone rather than
reduced, which is the property worth having: the win grows with repository size instead of
being a constant factor someone re-tunes later.

### Migrating what is already on disk

Existing `lexical.db` files have no companion table, and every one of their rows would be
unreachable by a rowid-based delete — permanently, silently, still being served by search.
`ensureLexicalSchema` backfills the table on first open and sweeps FTS rows that have no
companion entry (the old delete-by-chunk_id path could leave duplicates behind, and a
duplicate that no delete can find is a row that serves pre-edit code forever).

Pinned by [`daemon/lexicalstore_index_test.go`](../daemon/lexicalstore_index_test.go),
neuter-verified: with the backfill removed the migration test reports
`a legacy row survived DeleteByFilePath`, which is exactly the failure it exists to catch.

---

## Finding 2 — every re-index re-embedded the entire tree

`buildIndex` embedded every chunk the scan produced, on every run, unconditionally.
[`indexfreshness.go`](../daemon/indexfreshness.go) says so in its own header — "nothing is
hashed, nothing is re-embedded" — because its job is to *detect* staleness, not repair it.
Nothing else did either.

So the workflow was: edit three files, notice the index is stale, run `index`, wait
**2m15s** while 3,304 unchanged chunks were embedded a second time. Measured on the real
helper: ~40 ms/chunk, against ~90 µs for the lexical write and ~92 µs for the vector write.
**Embedding is >99% of index time**, and almost all of it was redundant.

`user` time was 27 minutes against 2m15s wall clock, so ONNX is already saturating all 12
cores. There is no threading win available here — the only way to make this faster is to
not do it.

### The fix

`carryOverUnchanged` ([`daemon/index_cmd.go`](../daemon/index_cmd.go)) asks the vector
store what it already holds under each chunk id and carries the embedding over when the
text is **byte-identical**. Only the remainder is batched into embedding calls.

Three things make it safe, and each is a way it could have silently served stale vectors:

- **Content, not id.** Chunk ids encode a line range (`path:40-79`), so an in-place edit
  that preserves length produces the same id over different text. Comparing ids alone would
  carry over the embedding of code that no longer exists. Neuter-verified: removing the
  content comparison makes the test report `the store still holds pre-edit text`.
- **Class too, separately.** Class is derived from the path by rules in this binary, so
  `classifyFile` can be corrected without any file changing. Content decides whether the
  vector is still valid; class decides whether the stored metadata is.
- **The embedder stamp gates the whole thing.** `indexWorkspace` asks
  `checkEmbedderStamp` **before** removing the stamp — the order is the safety property,
  since removing it first destroys the only record of which embedder built the prior index.
  Any mismatch at all (missing, corrupt, different id, different dimensionality, older
  chunk schema) means full re-embed.

### And then: don't rewrite what is already correct

Carrying the vector over still left a write per chunk. Phase-level measurement of a
full-reuse rebuild at n=3,000:

| phase | total | per chunk |
|---|---:|---:|
| `carryOverUnchanged` (one `GetByID` per chunk) | 4 ms | **1 µs** |
| vector `Upsert` (chromem, on disk) | 395 ms | 132 µs |
| lexical `Upsert` (delete + re-insert a trigram row) | **1.55 s** | **515 µs** |

The reuse lookup is free; **rewriting rows that were already correct was the whole
remaining cost.** So a chunk whose content and class both match, and which the lexical
store already has, is now skipped entirely.

The lexical store is asked directly rather than inferred from the vector store's answer:
they are separate databases and a build that ran while `lexical.db` was unavailable
populated only one of them. That case is pinned by
`TestReindexRewritesChunksMissingFromTheLexicalStore`.

That is what takes the re-index from 7.2s to **0.86s**.

### Partial builds still resume

Embed and upsert stay interleaved per batch rather than being split into an embed-everything
phase and an upsert-everything phase. The tidier-looking version is wrong: a failure in the
last batch would discard every vector the earlier batches had paid the model for, so a run
that died at 80/83 would re-embed all 83 next time. Upserting each batch as it lands means a
failed run leaves its completed work durable — and now that carry-over exists, the retry
picks that work straight back up. The partial index is still refused by
`checkEmbedderStamp`, because no stamp is written on failure: **durable is not the same as
trusted.** Pinned by the pre-existing
`TestIndexWorkspace_BatchFailureLeavesNoValidStampedIndex`, which caught this exact
regression during the work.

---

## Struck — vector search is not a lever at this scale

`ChromemStore.Query` is brute-force cosine similarity over every stored vector, which
looked like an obvious target. Measured:

| chunks | query |
|---:|---:|
| 500 | 0.96 ms |
| 2,000 | 2.40 ms |
| 8,000 | 13.1 ms |

Linear, as expected, and 13 ms at 8,000 chunks sits inside the ~14 ms the 2026-07-17 note
already attributed to whole-turn retrieval — against a ~1,450 ms TTFT dominated by provider
prefill. **No ANN index or quantisation here can produce a user-perceptible change** at
repository sizes this product actually sees. Struck as a latency item.

It is worth re-measuring if the chunk ceiling is ever raised substantially: the cost is
linear in corpus size, so a 100k-chunk workspace would put this at ~160 ms and change the
answer.

---

## What is still open

- **Orphaned chunks from deleted files.** `buildIndex` upserts what it scans and deletes
  nothing, so a file removed between builds keeps its chunks in the vector store. The
  scan no longer produces them, but nothing evicts them either. Not addressed here — it is
  a correctness question about index contents rather than a speed one, and it wants its own
  decision about whether `index` should be additive or authoritative.
- **Embedding batch padding.** `OnnxEmbedder.Embed` pads every sequence in a batch to the
  longest one in that batch, and attention is quadratic in sequence length. Sorting a batch
  by token length before inference would cut real compute on mixed-length batches. Unmeasured
  — it needs the token-length distribution of a real corpus before it is worth doing, and it
  only pays on the cold path, which is now the rare one.
