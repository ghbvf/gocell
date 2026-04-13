## 2026-04-12 - Postgres Batch Insertion Memory Optimization
**Learning:** `strconv.Itoa` causes thousands of unnecessary allocations inside large batch generation loops, and `strings.Builder` dynamically resizing wastes time.
**Action:** Use `sb.Grow` to pre-allocate capacity and `strconv.AppendInt` with a local buffer `[32]byte` to prevent allocations entirely during heavy string building operations.
## 2026-04-13 - Optimize SQL Query Builder String Formatting
**Learning:** `fmt.Sprintf("$%d", len(b.args))` inside a core utility like `pkg/query/builder.go` causes significant performance bottlenecks because it uses reflection and generates unnecessary heap allocations when run in hot loops.
**Action:** Use `strconv.Itoa` along with string concatenation for simple type conversions (e.g. integer to string) inside performance critical paths. Also, pre-allocate slices with a default capacity to reduce slice resize operations.
