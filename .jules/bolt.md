## 2026-04-12 - Postgres Batch Insertion Memory Optimization
**Learning:** `strconv.Itoa` causes thousands of unnecessary allocations inside large batch generation loops, and `strings.Builder` dynamically resizing wastes time.
**Action:** Use `sb.Grow` to pre-allocate capacity and `strconv.AppendInt` with a local buffer `[32]byte` to prevent allocations entirely during heavy string building operations.
## 2024-05-15 - Journey Catalog Performance Bottleneck
**Learning:** Found an O(N*M + N log N) bottleneck in `kernel/journey/catalog.go` where `CellJourneys`, `ContractJourneys`, and other read methods were re-scanning and sorting the entire map of journeys on every call.
**Action:** When seeing maps being converted to slices and sorted inside read methods, pre-compute and sort those slice indexes at initialization time to achieve O(1) lookups.
