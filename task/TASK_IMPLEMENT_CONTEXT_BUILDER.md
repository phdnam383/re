# Task triển khai Context Builder cho `re`

## 0. Cấu trúc thư mục mục tiêu

Tận dụng ranh giới kiến trúc cấp cao của IAE nhưng không sao chép nguyên cấu trúc và các lớp không còn cần thiết. Không import trực tiếp `IAE/internal/...` vì quy tắc `internal` của Go; phần phù hợp phải được copy và đơn giản hóa trong module `re`.

```text
re/
├── cmd/
│   └── engine/
│       └── main.go                 # Composition root
├── gen/
│   └── mdafv1/                     # Generated protobuf
├── internal/
│   ├── analysis/
│   │   ├── service.go              # Context → Rules → Response
│   │   └── types.go                # Shared domain models
│   │
│   ├── contextbuilder/
│   │   ├── builder.go              # Provider orchestration
│   │   ├── profile.go              # ContextProfile models
│   │   ├── selector.go             # Selector matching
│   │   ├── plan.go                 # Merge/deduplicate provider work
│   │   ├── ports.go                # Repository/provider interfaces
│   │   │
│   │   ├── postgres/
│   │   │   ├── profile.go          # Load enabled profiles
│   │   │   ├── vdu.go              # VDU + descendant VNFC query
│   │   │   └── link.go             # Exact directed-link query
│   │   │
│   │   └── configuration/
│   │       └── provider.go          # Direct REST GET
│   │
│   ├── ruleengine/                  # Task sau, ngoài phạm vi task này
│   │   ├── engine.go
│   │   ├── facts.go
│   │   ├── result.go
│   │   └── grule.go
│   │
│   └── transport/
│       └── grpc/                    # Task sau, ngoài phạm vi task này
│           ├── server.go
│           └── mapper.go
│
├── proto/
├── db/
├── context_profile/
├── grule/
├── testdata/
│   └── context_builder/
└── task/
```

- Dùng package `contextbuilder`, không dùng tên `context`, để không xung đột với standard library `context` và tránh alias `stdctx` như IAE.
- Task này tạo `internal/analysis/types.go` cho các model dùng chung và toàn bộ `internal/contextbuilder`.
- `cmd`, Analysis Service, Rule Engine và gRPC transport chỉ thể hiện vị trí kiến trúc mục tiêu; không triển khai trong task này.
- Có thể adapt từ IAE: selector matching, provider ports, parallel execution, deterministic aggregation, HTTP error handling và PostgreSQL repository conventions.
- Không mang sang: definition bundle/publisher, topology walker, limits/truncation, fallback plan, provider required/optional, Health Provider, RCA resolver và Decision Engine.

## 1. Contract và domain model

- Khởi tạo Go module `re` theo Go 1.26, dùng `database/sql` với pgx driver.
- Tạo domain types độc lập protobuf:
  - `ContextInput`: request ID, incident, alerts.
  - `ContextProfile`, `Selector`, `ProviderSpec`.
  - `VDU`, `VNFC`, `Link`, `ConfigurationEntry`.
  - `ContextSnapshot`: input, matched profile names, status, các collection context, missing context và built time.
- Dùng collection phẳng `VDUs[]`, `VNFCs[]`, liên kết qua `vdu_path`.
- Builder API:

```go
Build(context.Context, ContextInput) (ContextSnapshot, error)
```

- Mở rộng protobuf `MissingContext` với `string reason = 4`.
- Thêm `UNIQUE` cho `context_profile.name`.

## 2. Profile loading, selector và merged plan

- Tạo `ProfileRepository.LoadEnabled(ctx)`:
  - Query mọi profile `enabled = TRUE`, sort theo `name`.
  - Decode và validate toàn bộ JSONB; lỗi query/decode là lỗi build.
  - Context Builder tự load profile; Rule Engine load rules riêng.
- Selector semantics:
  - Profile match nếu tồn tại một alert tự nó thỏa mọi clause.
  - AND giữa probable cause, alert type và các additional-information key.
  - OR giữa các value trong một danh sách.
  - So sánh string không phân biệt hoa/thường; JSON key phân biệt hoa/thường.
  - `additional_information[key] = []` chỉ yêu cầu key tồn tại.
  - Danh sách không rỗng so sánh JSON scalar đúng kiểu; string dùng case-insensitive.
  - Selector `{}` là definition error.
- Merge mọi profile match bằng union/deduplicate:
  - VDU theo `path`.
  - Link theo `(src_path,dst_path)`.
  - Configuration theo `(path,key)`.
  - Cùng `(path,key)` nhưng khác URL là definition error.
- Validate provider key, LTREE path, link endpoint, configuration path/key và URL `http|https` trước khi gọi provider.
- Không profile match: trả typed error `ErrContextProfileNotFound` với thông điệp `missing context_profile`; không tạo snapshot và không gọi provider.
- Không có fallback plan, topology expansion hoặc context limits.

## 3. Provider pipeline và snapshot assembly

- Tạo ba provider ports và chạy song song khi merged plan có work; merge kết quả cố định theo `VDU → LINK → CONFIGURATION`.
- VDU Provider:
  - Bulk-query exact requested VDU paths.
  - Dùng `vnfc.path <@ vdu.path` để lấy mọi descendant VNFC.
  - Trả toàn bộ cột bảng `vdu` và `vnfc`; không join `managed_object` hay `vnfc_binding`.
  - VDU tồn tại nhưng không có VNFC là hợp lệ.
  - VDU không tồn tại tạo missing context.
- Link Provider:
  - Bulk-query đúng directed pair `(src_path,dst_path)`.
  - Không traversal, không tự lấy chiều ngược.
  - Trả toàn bộ cột bảng `link`; pair không tồn tại tạo missing.
- Configuration Provider:
  - Mỗi `{path,key,url}` thực hiện một GET độc lập.
  - Gọi mọi target song song, không giới hạn concurrency.
  - Timeout mặc định 2 giây/call; request deadline ngắn hơn thắng.
  - Body 2xx phải là một JSON value hợp lệ; lưu `path`, `key`, `url`, `value`, `read_at`.
  - Non-2xx, timeout, body rỗng hoặc JSON lỗi tạo missing cho target tương ứng.
- Provider lỗi tạo `PARTIAL`, ghi missing cho mọi target bị ảnh hưởng và giữ kết quả provider khác.
- Caller cancellation/deadline trả `ctx.Err()` thay vì `PARTIAL`.
- Sort deterministic:
  - Profiles và VDU/VNFC theo path/name.
  - Links theo src/dst.
  - Configuration theo path/key/url.
  - Missing theo thứ tự provider rồi entity/key.
- Dùng injectable clock để `built_at` và `read_at` có thể test deterministic.

## 4. Tests và tiêu chí hoàn thành

- Unit tests:
  - Selector AND/OR, case-insensitive, key-presence, typed scalar và selector rỗng.
  - Multi-profile union/dedup, configuration URL conflict và unknown provider.
  - Không profile match trả `ErrContextProfileNotFound`/`missing context_profile` và không gọi provider.
  - Provider fan-out song song, deterministic merge, missing/PARTIAL và cancellation.
- Provider tests:
  - VDU/Link integration với PostgreSQL thật qua `RE_TEST_DB_DSN`; apply schema/seed và skip khi DSN không có.
  - Kiểm tra LTREE lấy mọi descendant VNFC, exact directed link và missing rows.
  - Configuration dùng `httptest.Server` cho JSON scalar/object/array, timeout, non-2xx, invalid/empty body và partial success.
- Scenario tests cho SIPGW, DIAGW và TPS từ selector đến snapshot cuối.
- Lưu expected output dưới dạng golden JSON; dùng fixed clock và deterministic sorting.
- Chạy `go test ./...`; khi có PostgreSQL test, chạy lại với `RE_TEST_DB_DSN`.

## Giới hạn task

- Bao gồm Context Builder, domain types, provider implementations, profile repository, thay đổi `MissingContext` proto và uniqueness của profile name.
- Không bao gồm gRPC server, Rule Engine, GRL Facts/Result API hoặc response assembler.
- Chỉ tái sử dụng ý tưởng từ IAE: selector matching, provider ports, HTTP error handling và deterministic aggregation; không mang sang topology walker, limits, fallback plan, health provider, `StageResult` hay Decision Engine.
