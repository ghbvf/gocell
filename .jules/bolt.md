## 2026-04-12 - Postgres Batch Insertion Memory Optimization
**Learning:** `strconv.Itoa` causes thousands of unnecessary allocations inside large batch generation loops, and `strings.Builder` dynamically resizing wastes time.
**Action:** Use `sb.Grow` to pre-allocate capacity and `strconv.AppendInt` with a local buffer `[32]byte` to prevent allocations entirely during heavy string building operations.
