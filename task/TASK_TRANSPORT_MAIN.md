# Task triển khai Analysis Service, gRPC Transport và `main` cho `re`

## 0. Mục tiêu và cấu trúc thư mục

Task này nối hai module đã được triển khai bởi `TASK_IMPLEMENT_CONTEXT_BUILDER.md`
và `TASK_IMPLEMENT_RCA.md` thành một service chạy được đầu-cuối:

```text
gRPC request
    → map protobuf sang domain + validate
    → Context Builder
    → RCA Rule Engine
    → assemble domain result
    → map sang gRPC response
```

Giữ ranh giới kiến trúc cấp cao của IAE, nhưng chỉ mang sang composition root,
application orchestration, protobuf mapper, error mapping và graceful shutdown.
Không mang sang definition bundle/publisher, analysis persistence/replay, stage
budget, Decision Engine hoặc các entry point khác `AnalyzeIncident`.

```text
re/
├── cmd/
│   └── engine/
│       ├── main.go                    # Chỉ gọi run và xử lý exit code
│       ├── config.go                  # Đọc/validate environment
│       └── run.go                     # DB + modules + gRPC + shutdown wiring
│
├── internal/
│   ├── analysis/
│   │   ├── types.go                   # Shared domain models hiện có
│   │   ├── service.go                 # Context → RCA → AnalysisResult
│   │   ├── ports.go                   # ContextBuilder/RCAAnalyzer interfaces
│   │   ├── validation.go              # Domain request validation
│   │   └── status.go                  # Overall status derivation
│   │
│   ├── contextbuilder/                # Đã triển khai
│   ├── ruleengine/                    # Đã triển khai
│   │
│   └── transport/
│       └── grpc/
│           ├── server.go              # Generated server adapter
│           ├── mapper.go              # protobuf ↔ domain
│           ├── errors.go              # domain error → gRPC status
│           └── interceptor.go         # Structured request logging
│
├── gen/
│   └── mdafv1/                        # Generated protobuf hiện có
├── proto/
│   └── engine.proto
├── testdata/
│   └── engine/
│       ├── sipgw_link_down/
│       ├── diagw_link_down/
│       └── tps_overloaded/
└── README.md                           # Cách cấu hình, chạy và kiểm thử
```

- `internal/analysis` không import package implementation. Package này chỉ sở
  hữu domain types, ports và orchestration.
- `internal/contextbuilder`, `internal/ruleengine` và `internal/transport/grpc`
  được phép import `internal/analysis`.
- Chỉ `cmd/engine` import và khởi tạo các implementation cụ thể.
- Không import trực tiếp `IAE/internal/...`; phần phù hợp phải được adapt vào
  module `re` và đơn giản hóa theo contract hiện tại.

## 1. Analysis Service contract

### 1.1. Ports

Khai báo các interface tối thiểu trong `internal/analysis/ports.go` để service
không phụ thuộc implementation:

```go
type ContextBuilder interface {
    Build(context.Context, ContextInput) (ContextSnapshot, error)
}

type RCAAnalyzer interface {
    Analyze(context.Context, ContextSnapshot) (RCAResult, error)
}
```

`*contextbuilder.Builder` và `*ruleengine.Engine` phải thỏa trực tiếp hai
interface này; không tạo adapter nếu signatures đã khớp.

### 1.2. Domain result

Thêm domain result độc lập protobuf vào `internal/analysis/types.go`:

```go
type AnalysisResult struct {
    RequestID string
    Incident  string

    OverallStatus string
    ContextStatus string
    RCAStatus     string

    RootCauses    []RootCause
    MissingContext []MissingContext
}
```

- `AnalysisResult` chỉ chứa dữ liệu public cần để tạo
  `AnalyzeIncidentResponse`.
- Không đưa `RuleExecutions`, toàn bộ `ContextSnapshot`, latency nội bộ hoặc
  DB definition metadata ra protobuf.
- Mapper phải tạo collection mới khi cần; không cho transport sửa slice thuộc
  snapshot/RCA result.

### 1.3. Service API và orchestration

Triển khai:

```go
AnalyzeIncident(context.Context, ContextInput) (AnalysisResult, error)
```

Luồng cố định:

1. Kiểm tra `ctx.Err()` và validate `ContextInput`.
2. Gọi Context Builder đúng một lần.
3. Nếu build lỗi, dừng; không gọi RCA.
4. Gọi RCA với chính `ContextSnapshot` vừa build, kể cả snapshot `PARTIAL`.
5. Nếu RCA lỗi, không trả response thành công một phần.
6. Assemble `AnalysisResult` từ input, snapshot và `RCAResult`.

Không reload profile/rule trong service: Context Builder và RCA Engine đã tự
load definition ở đầu stage tương ứng. Không thêm transaction bao quanh hai
query; hai module cố ý nhận definition mới nhất tại thời điểm chúng chạy.

Không đặt request timeout toàn cục trong service. Deadline/cancellation từ
gRPC phải truyền nguyên vẹn qua cả hai stage; Configuration Provider và từng
RCA row đã có timeout ứng dụng riêng.

### 1.4. Validation

Tạo sentinel `ErrInvalidRequest` và trả lỗi wrap sentinel này với field cụ thể.
Validation tối thiểu:

- `request_id` không rỗng.
- `incident` không rỗng.
- Có ít nhất một alert.
- Mỗi alert có `id` và `source_path` không rỗng.
- Alert protobuf `nil` trong repeated field được coi là alert không hợp lệ,
  không được panic.

Không trim hoặc đổi hoa/thường dữ liệu đầu vào. `alert_type`,
`probable_cause`, severity, state và `additional_information` có thể thiếu;
selector quyết định profile có match hay không. `created_at` tiếp tục là opaque
string theo proto hiện tại, không parse lại trong task này.

### 1.5. Status derivation

`ContextStatus` lấy trực tiếp từ `ContextSnapshot.Status`; `RCAStatus` lấy trực
tiếp từ `RCAResult.Status`. Với một response thành công, `OverallStatus` bằng
trạng thái RCA vì RCA đã hạ xuống `PARTIAL` khi context partial:

| Context | RCA | Overall |
|---|---|---|
| `COMPLETE` | `COMPLETE` | `COMPLETE` |
| `COMPLETE` | `NO_CONCLUSION` | `NO_CONCLUSION` |
| `PARTIAL` | `PARTIAL` | `PARTIAL` |
| `COMPLETE` | `PARTIAL` | `PARTIAL` |

- Tổ hợp status không hợp lệ, ví dụ context `PARTIAL` nhưng RCA `COMPLETE`, là
  internal error thay vì âm thầm tạo response mâu thuẫn.
- RCA `FAILED` luôn đi kèm error và không được trả như business response.
- `MissingContext` lấy duy nhất từ `ContextSnapshot`; lỗi rule nằm trong log
  nội bộ, không giả làm missing context.

## 2. Protobuf mapper và gRPC server

### 2.1. Request mapper

`requestFromPB` map đầy đủ:

- `request_id`, `incident`.
- Mọi field của `Alert`.
- `google.protobuf.Struct additional_information` sang `map[string]any` bằng
  protobuf API, giữ đúng JSON type của string/number/bool/null/object/list.
- Struct nil thành nil hoặc map rỗng nhất quán; selector presence semantics
  không được làm một key hiện hữu biến mất.

Mapper chỉ chuyển representation. Domain validation nằm ở Analysis Service để
mọi caller ngoài gRPC sau này vẫn nhận cùng quy tắc.

### 2.2. Response mapper

Map `AnalysisResult` sang `AnalyzeIncidentResponse`:

- Echo đúng `request_id` và `incident` đã validate.
- `status.overall`, `status.context`, `status.rca` theo domain result.
- Map đầy đủ root cause và actions, giữ nguyên deterministic ordering do RCA
  Engine tạo.
- `RecommendedAction.value` dùng `structpb.NewValue`; JSON null phải map thành
  protobuf null, không thành field mất nghĩa.
- Map `provider`, `entity`, `key` và `reason` của mọi `MissingContext`.
- `meta.context_status` phải bằng `status.context`.

Nếu một action value không thể biểu diễn bằng protobuf Value, coi là internal
mapping error. Trường hợp này đáng lẽ đã bị Result sink từ chối, nhưng mapper
vẫn phải phòng thủ và không panic.

### 2.3. Server adapter

`Server` phải:

- Embed `mdafv1.UnimplementedIncidentAnalysisEngineServer`.
- Chỉ phụ thuộc interface nhỏ có method `AnalyzeIncident`, không phụ thuộc
  concrete `*analysis.Service`.
- Truyền nguyên `context.Context` từ RPC vào service.
- Map request, gọi service, map response và chuyển error qua `toStatusError`.
- Có `Register(grpc.ServiceRegistrar)` để `main` không biết generated register
  function.

Không đặt business logic, DB access, retry hoặc timeout mới trong server.

### 2.4. Error mapping

Mapping gRPC bắt buộc:

| Domain error | gRPC code |
|---|---|
| `context.Canceled` | `Canceled` |
| `context.DeadlineExceeded` | `DeadlineExceeded` |
| `analysis.ErrInvalidRequest` | `InvalidArgument` |
| `contextbuilder.ErrContextProfileNotFound` | `FailedPrecondition` |
| `ruleengine.ErrRCARuleNotFound` | `FailedPrecondition` |
| Lỗi còn lại | `Internal` |

- Phải dùng `errors.Is`/`errors.As`, vì service sẽ wrap lỗi theo stage.
- Hai lỗi thiếu definition giữ message ổn định `missing context_profile` và
  `missing rca_rule` để caller biết engine chưa đủ dữ liệu cấu hình.
- Với lỗi `Internal`, log full error ở server/interceptor nhưng trả message
  tổng quát cho client; không làm lộ DSN, SQL, URL credential hoặc GRL content.
- Business outcomes `PARTIAL` và `NO_CONCLUSION` là response thành công, không
  map thành gRPC error.

### 2.5. Logging interceptor

Thêm unary interceptor dùng `slog`:

- Log method, request ID, duration và final gRPC code.
- Khi thành công, log overall/context/RCA status.
- Khi lỗi, log error đầy đủ ở phía server.
- Không log toàn request, alert `additional_information`, context values, rule
  content hoặc action value.
- Không thay đổi response/error và không nuốt panic. Panic recovery, tracing và
  metrics backend nằm ngoài phạm vi task này.

## 3. Composition root và runtime configuration

### 3.1. Environment contract

`cmd/engine/config.go` đọc cấu hình một lần khi khởi động:

| Biến | Bắt buộc | Mặc định | Ý nghĩa |
|---|---:|---:|---|
| `RE_DB_DSN` | Có | — | PostgreSQL chứa topology, profiles và RCA rules |
| `RE_GRPC_ADDR` | Có | — | Địa chỉ listen, ví dụ `:30051` |
| `RE_CONFIGURATION_TIMEOUT` | Không | `2s` | Timeout cho mỗi Configuration GET |
| `RE_RCA_RULE_TIMEOUT` | Không | `800ms` | Timeout cho mỗi `rca_rule` row |

- Duration dùng cú pháp `time.ParseDuration`; giá trị rỗng dùng default, giá
  trị không parse được hoặc `<= 0` làm startup fail.
- Không thêm `RE_DB_DRIVER`; driver cố định là `pgx`.
- Không nhận base URL cho Configuration Provider: URL đã nằm trong từng
  `context_profile` target.
- Không hard-code DSN hoặc địa chỉ môi trường development trong binary.

### 3.2. Startup wiring

Thứ tự khởi tạo trong `run.go`:

1. Load và validate config.
2. `sql.Open("pgx", RE_DB_DSN)` và `PingContext` với startup timeout hữu hạn
   (đề xuất 5 giây); DB không sẵn sàng thì fail fast.
3. Tạo một shared `*sql.DB` pool.
4. Tạo Profile Repository, VDU Provider, Link Provider và Configuration
   Provider.
5. Tạo Context Builder.
6. Tạo RCA Rule Repository và Rule Engine.
7. Tạo Analysis Service.
8. Tạo `grpc.Server`, gắn logging interceptor và register transport Server.
9. `net.Listen` rồi serve cho tới khi process nhận signal hoặc server lỗi.

Wiring dự kiến:

```text
*sql.DB
├── contextbuilder/postgres.ProfileRepository
├── contextbuilder/postgres.VDUProvider
├── contextbuilder/postgres.LinkProvider
└── ruleengine/postgres.RuleRepository

http.Client + timeout
└── contextbuilder/configuration.Provider

Context Builder + Rule Engine
└── analysis.Service
    └── transport/grpc.Server
```

- Dùng một `slog.Logger` JSON ghi stdout và inject cùng logger vào Builder,
  Rule Engine và transport.
- `main.go` phải nhỏ: gọi hàm `run`, log lỗi cuối và trả exit code khác 0.
- Không apply migration/seed khi engine startup. Schema và seed là bước deploy
  riêng; binary chỉ kiểm tra kết nối DB.
- `defer db.Close()` và đóng listener theo lifecycle của process.

### 3.3. Graceful shutdown

- Dùng `signal.NotifyContext` cho `os.Interrupt` và `SIGTERM`.
- Khi signal đến, ngừng nhận request mới và gọi `GracefulStop` để request đang
  chạy có cơ hội hoàn thành.
- Đặt shutdown grace period hữu hạn (đề xuất 10 giây); hết thời gian thì gọi
  `Stop` để process không treo vô hạn.
- Nếu `Serve` tự trả lỗi trước signal, trả lỗi đó khỏi `run`.
- Đảm bảo goroutine phục vụ và shutdown không leak/deadlock trong tests.

## 4. Tests

### 4.1. Analysis Service unit tests

- Validation cho từng required field và alert nil/không hợp lệ.
- Happy path gọi Builder rồi RCA đúng một lần, đúng thứ tự và RCA nhận chính
  snapshot Builder trả về.
- Builder lỗi thì RCA không được gọi.
- Snapshot `PARTIAL` vẫn được đưa vào RCA và tạo response `PARTIAL` với đầy đủ
  missing context.
- RCA `NO_CONCLUSION` tạo response thành công, root causes rỗng.
- RCA error/cancellation/deadline được propagate và không tạo response.
- Status matrix và các tổ hợp status bất hợp lệ.

### 4.2. Mapper tests

- Request map đủ alert fields và nested additional-information với mọi JSON
  type.
- Nil request/nil alert không panic và cuối cùng trả validation error.
- Response map đủ root causes/actions, JSON scalar/object/array/null và giữ
  order.
- MissingContext map đủ `reason`; `meta.context_status` khớp status context.
- Value không representable được trả lỗi thay vì panic.

### 4.3. gRPC transport tests

Dùng `bufconn` để chạy generated client/server thật trong memory:

- Happy path response shape.
- `InvalidArgument` cho request validation lỗi.
- `FailedPrecondition` cho thiếu matching context profile và không có enabled
  RCA rule.
- `Canceled` và `DeadlineExceeded` giữ đúng code.
- Internal dependency/mapping error trả `Internal` và không lộ error nhạy cảm.
- `PARTIAL`/`NO_CONCLUSION` vẫn có gRPC code `OK`.

### 4.4. Main/config tests

- Required env missing, duration default, duration invalid và duration `<= 0`.
- DB ping/listen failure được trả về thay vì `log.Fatal` sâu trong helper.
- Serve dừng sạch khi shutdown context bị cancel.
- Constructor/wiring lỗi làm startup fail trước khi nhận traffic.

### 4.5. End-to-end tests

Tạo test đầu-cuối qua generated gRPC client:

```text
protobuf request
→ gRPC transport
→ Analysis Service
→ PostgreSQL Context Builder
→ HTTP configuration stub
→ PostgreSQL Rule Repository + GRL runtime
→ protobuf response
```

- Dùng PostgreSQL thật khi có `RE_TEST_DB_DSN`; không có DSN thì skip test tích
  hợp DB, không skip unit/bufconn tests.
- Mỗi test dùng schema/transaction hoặc PostgreSQL schema riêng để không sửa DB
  dùng chung; apply `db/schema.sql` và seed cần thiết.
- Dùng `httptest.Server` và sửa URL profile trong test data để Configuration
  Provider gọi endpoint thật.
- Golden scenarios tối thiểu: SIPGW link down, DIAGW link down và TPS
  overloaded.
- Thêm failure scenarios: thiếu profile, thiếu rule, context partial, một rule
  row lỗi nhưng row khác thành công, mọi rule row lỗi, no conclusion và caller
  deadline.
- Golden response không chứa timestamp/latency nên phải so sánh strict toàn bộ
  protobuf JSON sau khi canonicalize.

## 5. Tooling, tài liệu và tiêu chí hoàn thành

- `buf lint` pass.
- `buf generate` tạo lại `gen/mdafv1` khớp `proto/engine.proto`; generated code
  phải được commit và không sửa tay.
- `go test ./...` pass.
- `go test -race ./...` pass.
- Chạy lại integration/E2E với `RE_TEST_DB_DSN` và xác nhận không bị skip.
- `go vet ./...` không có lỗi mới.
- Binary khởi động được với DB đã apply schema/seed, nhận request bằng
  `grpcurl`, và dừng sạch bằng SIGINT/SIGTERM.
- `README.md` ghi tối thiểu:
  - environment variables và defaults;
  - cách apply schema/seed;
  - lệnh chạy engine;
  - ví dụ `grpcurl` cho `AnalyzeIncident`;
  - cách chạy unit và PostgreSQL integration tests;
  - ý nghĩa `COMPLETE`, `NO_CONCLUSION`, `PARTIAL` và các gRPC errors chính.

## 6. Thứ tự triển khai đề xuất

1. Bổ sung Analysis Service ports, result, validation và status derivation.
2. Viết protobuf request/response mapper.
3. Viết gRPC server và error mapping.
4. Thêm logging interceptor.
5. Viết config loader và composition root.
6. Thêm graceful shutdown.
7. Hoàn thiện unit/bufconn tests.
8. Thêm PostgreSQL + HTTP + GRL end-to-end golden tests.
9. Regenerate protobuf, chạy toàn bộ checks và viết runbook.

## Giới hạn task

- Không sửa logic selector/provider của Context Builder hoặc Facts/GRL/result
  semantics của RCA, trừ lỗi integration được test chứng minh.
- Không thêm Definition Bundle, hot reload/publisher, rule selector, Decision
  Engine, Action Catalog hoặc remediation executor.
- Không thêm analysis persistence, request replay/idempotency store hoặc audit
  database.
- Không thêm REST ingress; REST trong thiết kế hiện tại chỉ là outbound GET của
  Configuration Provider.
- Không thêm authentication/authorization, TLS, reflection, gRPC health API,
  distributed tracing, metrics backend hoặc deployment manifests trong task
  này.
- Không đổi protobuf public contract ngoài việc regenerate code từ
  `proto/engine.proto` hiện có.
