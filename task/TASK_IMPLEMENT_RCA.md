# Task triển khai RCA Rule Engine cho `re`

## 0. Cấu trúc thư mục mục tiêu

Tận dụng kiến trúc lõi của RCA trong IAE nhưng triển khai thành package độc lập `ruleengine`. Không import trực tiếp `IAE/internal/...` vì quy tắc `internal` của Go; các phần phù hợp phải được copy, đổi sang domain model của `re` và loại bỏ resolver/ranking/Decision không còn dùng.

```text
re/
├── cmd/
│   └── engine/
│       └── main.go                    # Composition root, task integration sau
├── gen/
│   └── mdafv1/                        # Generated protobuf
├── internal/
│   ├── analysis/
│   │   ├── service.go                 # Context → RCA → Response, task sau
│   │   └── types.go                   # ContextSnapshot + RCA domain models
│   │
│   ├── contextbuilder/                # Được triển khai bởi task trước
│   │
│   ├── ruleengine/
│   │   ├── engine.go                  # Analyze API và status derivation
│   │   ├── ports.go                   # RuleRepository interface
│   │   ├── facts.go                   # GRL-facing Facts API
│   │   ├── view.go                    # Immutable indexed ContextSnapshot view
│   │   ├── result.go                  # Root-cause/action sink và merge
│   │   ├── runtime.go                 # Runtime và RuleExecution contracts
│   │   ├── grule.go                   # GRL compile/bind/execute/retract
│   │   ├── cache.go                   # Thread-safe content-addressed cache
│   │   ├── runner.go                  # Sequential execution/failure isolation
│   │   │
│   │   └── postgres/
│   │       └── rule.go                # Load enabled rca_rule rows
│   │
│   └── transport/
│       └── grpc/                      # Task sau, ngoài phạm vi task này
│           ├── server.go
│           └── mapper.go
│
├── proto/
├── db/
├── context_profile/
├── grule/
├── testdata/
│   └── rca/
│       ├── link_to_sipgw_down/
│       ├── link_to_diagw_down/
│       └── tps_overloaded/
└── task/
```

- Task này triển khai toàn bộ `internal/ruleengine`, PostgreSQL Rule Repository, RCA domain models còn thiếu và RCA testdata.
- `cmd`, Analysis Service, gRPC transport và response mapper chỉ thể hiện vị trí kiến trúc mục tiêu; không triển khai trong task này.
- Có thể adapt từ IAE: KnowledgeLibrary/cache/clone, DataContext binding, sequential runner, immutable Facts/view, failure isolation và result ordering.
- Không mang sang: rule resolver/selector, capability filtering, Subject iteration, RequiredContext, RCA limits, Finding/Evidence, ranking, Action Catalog và Decision Engine.

## 1. Contract và domain model

- Thêm các domain types dùng chung vào `internal/analysis/types.go`:
  - `RuleDefinition`.
  - `RCAResult`.
  - `RootCause`.
  - `RecommendedAction`.
  - `RuleExecution`.
- RCA API:

```go
Analyze(context.Context, analysis.ContextSnapshot) (analysis.RCAResult, error)
```

- `RCAResult` là nguồn duy nhất cho `Status`, `RootCauses` và internal `RuleExecutions`.
- `RuleExecutions` dùng cho logs và tests, không được thêm vào protobuf.
- Kết quả public map vào `RootCauseAnalysis`, `RecommendedAction` và `AnalysisStatus.rca` hiện có.
- RCA tự load rules ở đầu mỗi request; caller không truyền rule list.
- Không có enabled rule trả typed error `ErrRCARuleNotFound` với thông điệp `missing rca_rule`.

## 2. Rule Repository, compile cache và runtime

- Tạo `RuleRepository.LoadEnabled(ctx)` dùng query:

```sql
SELECT id, name, description, rule_content, salience, updated_at
FROM rca_rule
WHERE enabled = TRUE
ORDER BY salience DESC, name ASC;
```

- Một DB row là một scenario document và được phép chứa nhiều GRL rules có salience riêng.
- Query failure là request-level error trước execution.
- Empty content hoặc compiled KnowledgeBase không chứa rule là compile failure của row.
- Dùng thread-safe lazy cache theo `rule.ID + SHA-256(rule_content)`:
  - Hash không đổi thì reuse compiled template.
  - Cùng ID nhưng hash đổi thì compile và thay entry cũ, không tích lũy version cũ vô hạn.
  - Compile error không được cache như một entry thành công.
  - Mỗi execution clone KnowledgeBase vì working memory là mutable.
- Bind đúng hai facts `Ctx` và `Result` vào Grule DataContext.
- Bật `ReturnErrOnFailedRuleEvaluation` để evaluation error không bị hiểu nhầm là rule không match.
- Sau `Result.Assert`, runtime retract current GRL rule khỏi cloned KnowledgeBase. Không gọi `DataContext.Complete()`, vì một scenario document phải chạy được nhiều rules.
- `MaxCycle` bằng số GRL rules trong document. Rule fire nhưng không tạo root cause sẽ chạm cycle guard và làm row `FAILED`.

## 3. Facts API

- `Facts` là read-only facade trên immutable `ContextSnapshot`:

```go
type Facts struct {
    Alerts        *AlertFacts
    VDU           *VDUFacts
    VNFC          *VNFCFacts
    Link          *LinkFacts
    Configuration *ConfigurationFacts
}
```

- Build indexes một lần khi tạo Facts:
  - VDU path → VDU.
  - VNFC path → VNFC.
  - VDU path → descendant VNFCs.
  - `(src_path,dst_path)` → Link.
  - `(path,key)` → ConfigurationEntry.
- Expose đúng API đang được các GRL mẫu sử dụng:
  - `Alerts.HasCause`, `HasOverload`, `OverloadCount`.
  - `VDU.IsDegraded`, `ReadyReplicas`, `DesiredReplicas`.
  - `VNFC.Status`, `IsDown`.
  - `Link.Status`, `IsDown`.
  - `Configuration.Has`, `GetFloat`, `GetString`.
- Semantics:
  - Overload là alert tại entity hoặc descendant, cause `THRESHOLD_CROSSING`, metric bắt đầu bằng `overload`.
  - VNFC chỉ down khi status là `TERMINATED`; missing/UNKNOWN trả false.
  - VDU degraded khi `replicas > 0` và số descendant VNFC `RUNNING` nhỏ hơn replicas.
  - Link status `DOWN` hoặc `DEGRADED` được coi là down.
  - Enum, cause, status và metric so sánh không phân biệt hoa thường; LTREE path so sánh chính xác.
  - `Configuration.Has` kiểm tra entry tồn tại, kể cả JSON null.
  - `GetFloat`/`GetString` trả zero value khi thiếu hoặc sai kiểu; GRL phải dùng `Has` trước khi getter có zero value hợp lệ.
- Facts không được giữ DB handle, HTTP client hoặc cung cấp mutation method cho GRL.

## 4. Result sink và deterministic merge

- `Result` expose đúng signatures đang dùng trong GRL:

```go
Assert(id, category, summary, entity, role string, confidence float64)

Recommend(rootCauseID, code, moInstance, op string, value any)
```

- Validate chặt:
  - Root-cause ID, category, summary và entity không rỗng.
  - Role thuộc `PRIMARY | CONTRIBUTING | SUSPECTED`.
  - Confidence thuộc `0..1`.
  - Action code, root-cause ID và MO instance không rỗng.
  - Operation thuộc `ADD | REMOVE | REPLACE`.
  - Action chỉ được tham chiếu root cause đã được khai báo trong chính DB row đó.
- Validation error được giữ trong sink; sau execution, row chuyển thành `FAILED`. Không clamp và không âm thầm bỏ output sai.
- Mỗi row dùng sink tạm và là atomic failure boundary:
  - Chỉ merge khi toàn document execute thành công và sink không có lỗi.
  - Compile/evaluation/cycle/validation/merge conflict làm discard toàn bộ output của row.
- Root-cause ID trùng:
  - Metadata giống nhau thì idempotent và union actions.
  - Metadata khác nhau là conflict; row mới bị discard/FAILED.
- Action deduplicate theo `(code, mo_instance, op, canonical JSON value)`; action trùng nhau được hấp thụ, không liệt kê lại.
- Root causes giữ thứ tự fire đầu tiên.
- Actions sort theo code, tie-break theo MO instance, operation và canonical JSON value.

## 5. Runner, timeout và status

- Chạy DB rows tuần tự theo `salience DESC, name ASC`; không fan-out song song.
- Mỗi row có timeout ứng dụng mặc định 800 ms; request context/deadline ngắn hơn luôn thắng.
- Context `PARTIAL` vẫn chạy mọi rule. Facts trả false/zero cho dữ liệu thiếu và RCA result không được báo `COMPLETE`.
- Row compile hoặc execute lỗi được ghi internal `FAILED`; các row khác tiếp tục.
- Khi request hết deadline:
  - Dừng trước khi bắt đầu row kế tiếp.
  - Ghi các row còn lại internal `SKIPPED`.
  - Trả `ctx.Err()`.
- `RuleExecution` lưu tối thiểu rule ID/name, status `COMPLETE|FAILED|SKIPPED`, error, root-cause count và latency.
- Status derivation:
  - `COMPLETE`: có ít nhất một cause, context complete và không row failed.
  - `NO_CONCLUSION`: context complete, mọi row chạy thành công và không có cause.
  - `PARTIAL`: context partial hoặc có ít nhất một row failed; có thể không có cause.
  - `FAILED`: repository lỗi, không có enabled rules, hoặc không row nào compile/execute thành công; trả kèm error.
- Khi một số rows failed nhưng còn ít nhất một row chạy thành công, trả `RCAResult{Status: PARTIAL}` với nil error.

## 6. Tests và tiêu chí hoàn thành

- Facts unit tests:
  - Cause matching và entity/descendant scoping.
  - Overload metric matching.
  - VDU ready/desired/degraded.
  - VNFC/link status semantics.
  - Configuration present, numeric/string/null và wrong-type values.
- Result tests:
  - Field/range/enum validation.
  - Action tham chiếu cause chưa tồn tại.
  - Idempotent duplicate, metadata conflict và action dedup.
  - Root-cause/action deterministic ordering.
- GRL runtime/cache tests:
  - Nhiều matching rules trong cùng document fire đúng một lần theo salience.
  - Non-matching rules không sinh output.
  - Auto-retract không dừng các rules còn lại.
  - Rule fire nhưng không tạo cause chạm cycle guard.
  - Cache hit cho cùng hash; cùng ID đổi content thay entry cũ.
  - Concurrent cache access không compile trùng hoặc race.
- Runner/engine tests:
  - Sequential row order và per-row timeout.
  - Compile/execute failure isolation.
  - Atomic discard của row lỗi.
  - Cancellation đánh dấu remaining rows SKIPPED.
  - `COMPLETE`, `NO_CONCLUSION`, `PARTIAL`, `FAILED` derivation.
- PostgreSQL integration qua `RE_TEST_DB_DSN`:
  - Apply schema/seed trong test database.
  - Kiểm tra enabled filtering và `salience DESC, name ASC`.
  - Skip integration test khi DSN không được cấu hình.
- Scenario golden tests:
  - SIPGW Down.
  - DIAGW Down.
  - TPS Overloaded.
  - Assert toàn bộ root causes, roles, confidence và recommended actions.
- Inventory test bảo đảm normalized content trong `re/grule/*.grl` khớp `rca_rule.rule_content` trong seed.
- Chạy:

```text
go test ./...
go test -race ./...
```

## Giới hạn task

- Context Builder và shared `ContextSnapshot` đã hoàn thành theo task trước.
- Không triển khai gRPC server, Analysis Service hoặc response mapper.
- Không thay schema `rca_rule` và không thêm rule bundle/versioning.
- Không thêm rule selector, capability declarations, Subject iteration, ranking hoặc Decision stage.
- Không giới hạn số root causes/actions; chỉ giữ per-row timeout và GRL cycle guard để bảo vệ runtime.
