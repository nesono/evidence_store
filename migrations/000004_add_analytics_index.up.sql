-- The analytics aggregation filters by repo and a time window, then groups by
-- procedure_ref and walks each group in finished_at order. Before this index the
-- planner bitmap-ANDed idx_evidence_repo with idx_evidence_finished_at and then
-- visited 32k heap blocks, and the ordering had to be re-established with an
-- external merge sort that spilled to disk.
--
-- Leading with the group key rather than the time column is what earns most of
-- the win: it supplies the GROUP BY and the DISTINCT ON ordering directly. The
-- INCLUDE columns are what make the scan index-only -- measured without them the
-- planner ignored this index entirely and fell back to the bitmap scan.
--
-- Measured on 1,000,000 rows (12 repos, 88 procedures, 70k rows matching a
-- 90-day window):
--
--     no index                              450 ms
--     (repo, finished_at) INCLUDE (...)     328 ms   155 MB
--     this index, without INCLUDE           450 ms    79 MB  (planner ignores it)
--     this index                            197 ms   149 MB  Heap Fetches: 0
--
-- The cost is index size: roughly half the table's own size again. Deployments
-- that do not use the analytics endpoints can drop this index; the queries still
-- work, just at the 450 ms figure above.
CREATE INDEX idx_evidence_analytics
    ON evidence (repo, procedure_ref, finished_at DESC)
    INCLUDE (result, rcs_ref, id);
