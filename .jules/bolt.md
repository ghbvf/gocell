## 2026-04-12 - Postgres Batch Insertion Memory Optimization
**Learning:** `strconv.Itoa` causes thousands of unnecessary allocations inside large batch generation loops, and `strings.Builder` dynamically resizing wastes time.
**Action:** Use `sb.Grow` to pre-allocate capacity and `strconv.AppendInt` with a local buffer `[32]byte` to prevent allocations entirely during heavy string building operations.

## 2026-04-12 - Memory Allocation in Metric Collection
**Learning:** `fmt.Sprintf` uses reflection and dynamically allocates memory, which is a major bottleneck when called synchronously on every HTTP request for metric collection.
**Action:** Replace `fmt.Sprintf` with direct string concatenation and use pre-allocated/mapped strings (like `http.StatusOK` -> "200") to completely eliminate allocations in high-frequency metric paths.
