# Nhật ký (logs) của engine `re`

Tài liệu này tổng hợp mọi log mà engine phát ra, kèm điều kiện phát sinh và một
ví dụ JSON cho từng loại. Nguồn tham chiếu để vận hành và debug.

## Định dạng chung

Engine dùng `log/slog` với JSON handler ghi ra `stdout`, khởi tạo tại
[`cmd/engine/main.go`](../cmd/engine/main.go) (`slog.NewJSONHandler(os.Stdout, nil)`).
Mỗi dòng JSON có dạng:

```json
{"time":"<RFC3339Nano>","level":"<INFO|WARN|ERROR>","msg":"<thông điệp>","<field>":"<value>", ...}
```

Các trường phụ được thêm theo đúng thứ tự trong code. Dưới đây, các ví dụ đã được
rút gọn trường `time` cho dễ đọc.

> **Lưu ý về bảo mật:** lệnh `gRPC Internal error` chỉ mang message chung chung.
> Thông tin nhạy cảm (DSN, SQL, URL, nội dung rule) **chỉ** được log server-side,
> không bao giờ ra wire — khớp với cam kết ghi trong [`CLAUDE.md`](../CLAUDE.md).

Các file phát sinh log:

| File | Vai trò |
| --- | --- |
| [`cmd/engine/main.go`](../cmd/engine/main.go) | Startup, shutdown, lỗi cấu hình |
| [`internal/transport/grpc/interceptor.go`](../internal/transport/grpc/interceptor.go) | Log cho **mọi** RPC |
| [`internal/transport/grpc/server.go`](../internal/transport/grpc/server.go) | Log khi handler trả error |
| [`internal/contextbuilder/builder.go`](../internal/contextbuilder/builder.go) | Build snapshot, lỗi từng provider |
| [`internal/ruleengine/engine.go`](../internal/ruleengine/engine.go) | Rule không hoàn thành |
| [`internal/analysis/service.go`](../internal/analysis/service.go) | Rule không hoàn thành (trùng với rule engine) |

---

## 1. Startup & shutdown — `cmd/engine/main.go`

### `engine listening` (Info)
Sau khi bind port thành công, ngay trước khi bắt đầu serve. Bao gồm các timeout
đang áp dụng.

```json
{"time":"2026-09-04T08:30:12.481+07:00","level":"INFO","msg":"engine listening","address":"[::]:50051","configuration_timeout":"3s","probe_timeout":"2s","vdu_timeout":"5s","metric_timeout":"5s","rca_rule_timeout":"2s"}
```

### `shutdown signal received` (Info)
Nhận `SIGINT` hoặc `SIGTERM`, bắt đầu graceful stop.

```json
{"time":"2026-09-04T08:35:01.102+07:00","level":"INFO","msg":"shutdown signal received","grace_period":"10s"}
```

### `shutdown complete` (Info)
Graceful stop kết thúc trước khi hết grace period (10s).

```json
{"time":"2026-09-04T08:35:03.778+07:00","level":"INFO","msg":"shutdown complete"}
```

### `grace period expired; forcing shutdown` (Warn)
Grace period 10s trôi qua mà vẫn còn RPC in-flight → gọi `server.Stop()` (ép dừng).

```json
{"time":"2026-09-04T08:35:11.481+07:00","level":"WARN","msg":"grace period expired; forcing shutdown"}
```

### Lỗi cấu hình / DB (stderr text — KHÔNG phải slog)

Các lỗi khởi tạo (config, mở DB, ping DB, build từng component, listen, serve)
không đi qua `slog`; chúng ghi text ra `stderr` rồi `os.Exit(1)`:

```
engine: configuration: DATABASE_URL is required
engine: connect to database: dial tcp 10.0.0.5:5432: connect: connection refused
engine: listen on :50051: listen tcp :50051: bind: address already in use
```

---

## 2. Mọi gRPC RPC — `internal/transport/grpc/interceptor.go`

`LoggingInterceptor` gói toàn bộ RPC, log một dòng cho mỗi lần gọi.

### `rpc completed` (Info)
Handler trả về thành công. Ngoài các trường chung, có thêm thống kê kết quả
(trích từ response).

```json
{"time":"2026-09-04T08:31:44.219+07:00","level":"INFO","msg":"rpc completed","method":"/mdaf.fault.v1.FaultAnalysisService/AnalyzeAlertByRule","request_id":"req-7f3a9c","duration_ms":318,"code":"OK","overall_status":"COMPLETE","context_status":"COMPLETE","rca_status":"COMPLETE","root_causes":1,"missing_context":0}
```

 Khi context bị thiếu (PARTIAL):

```json
{"time":"...","msg":"rpc completed","code":"OK","overall_status":"PARTIAL","context_status":"PARTIAL","rca_status":"PARTIAL","root_causes":0,"missing_context":2}
```

### `rpc failed` (Error)
Handler trả error. **Không có** trường thống kê kết quả (vì không có response).
`code` luôn khác `OK`.

```json
{"time":"2026-09-04T08:32:01.555+07:00","level":"ERROR","msg":"rpc failed","method":"/mdaf.fault.v1.FaultAnalysisService/AnalyzeAlertByRule","request_id":"req-7f3a9c","duration_ms":12,"code":"Internal"}
```

---

## 3. Handler gRPC — `internal/transport/grpc/server.go`

Chỉ log khi `AnalyzeAlertByRule` phải trả error về client (lỗi build context,
lỗi chạy rule, hoặc lỗi map response). Đây là nơi log **full error** server-side.

### `analyze alert failed` (Error)

```json
{"time":"2026-09-04T08:32:01.555+07:00","level":"ERROR","msg":"analyze alert failed","request_id":"req-7f3a9c","error":"analysis: build context: contextbuilder: profile repository: query failed: failed to connect to host=localhost user=re (server error: FATAL: password authentication failed for user \"re\")"}
```

---

## 4. Context Builder — `internal/contextbuilder/builder.go`

### `context snapshot built` (Info)
Sau khi fan-out tới các provider xong, log cho **mọi** request. Trường `snapshot`
chứa toàn bộ object snapshot (alerts, vdus, vnfcs, links, configuration…).

```json
{"time":"2026-09-04T08:31:44.201+07:00","level":"INFO","msg":"context snapshot built","request_id":"req-7f3a9c","status":"COMPLETE","snapshot":{"alerts":[...],"vdus":[...],"vnfcs":[...],"links":[...],"configuration":[...]}}
```

### Bốn log provider failed (Warn) — chạy song song
Mỗi provider chạy trong một goroutine riêng. VDU, Configuration và Metric log
khi cả provider trả về lỗi. Link Provider còn log riêng từng target không thu
thập được; các target khác vẫn có thể thành công. Target lỗi được đánh dấu
`missing` với reason tương ứng:

| Provider | Lý do (`Reason`) | Ví dụ thông điệp + trường |
| --- | --- | --- |
| `VDU` | `QUERY_FAILED` | `vdu provider failed` |
| `CONFIGURATION` | `REQUEST_FAILED` | `configuration provider failed` |
| `LINK` | reason cụ thể của từng target | `link provider failed` |
| `METRIC` | `REQUEST_FAILED` | `metric provider failed` |

**vdu provider failed** (lỗi query DB):

```json
{"time":"2026-09-04T08:31:44.200+07:00","level":"WARN","msg":"vdu provider failed","provider":"VDU","targets":2,"error":"query vdu: connection reset"}
```

**configuration provider failed** (lỗi call HTTP tới NF config API):

```json
{"time":"2026-09-04T08:31:44.201+07:00","level":"WARN","msg":"configuration provider failed","provider":"CONFIGURATION","targets":3,"error":"context deadline exceeded"}
```

**link provider failed** (lỗi probe HTTP):

```json
{"time":"2026-09-04T08:31:44.200+07:00","level":"WARN","msg":"link provider failed","provider":"LINK","vnfc_path":"ims.vdu_sb_logic.vnfc_sb_logic_1","target":"10.10.0.5","reason":"REQUEST_FAILED","error":"Post \"http://prf/api/v1/probe/ping\": dial tcp: connect: connection refused"}
```

Mỗi log Link luôn có `vnfc_path`, `target` và `reason`. Lỗi transport/decode có
thêm `error`; response non-2xx có thêm `http_status`.

**metric provider failed** (lỗi truy metric):

```json
{"time":"2026-09-04T08:31:44.201+07:00","level":"WARN","msg":"metric provider failed","provider":"METRIC","targets":2,"error":"http status 503: service unavailable"}
```

---

## 5. Rule không hoàn thành (⚠️ log TRÙNG ở 2 nơi)

Khi một rule `rca_rule` kết thúc với trạng thái `FAILED` hoặc `SKIPPED`, sự kiện
đó được ghi **hai lần** với cùng `rule_id`/`rule_name`/`status`/`error`. Bản ở
analysis service đầy đủ hơn vì có thêm `request_id`.

### Bản ở rule engine — `internal/ruleengine/engine.go`
**Không có** `request_id`:

```json
{"time":"2026-09-04T08:31:44.218+07:00","level":"WARN","msg":"rca rule did not complete","rule_id":"R-VNFC-DOWN","rule_name":"VNFC marked down: check peer SIP gateway","status":"FAILED","error":"grule: rule timeout after 2s"}
```

### Bản ở analysis service — `internal/analysis/service.go`
**Có thêm** `request_id` (đầy đủ hơn):

```json
{"time":"2026-09-04T08:31:44.218+07:00","level":"WARN","msg":"rca rule did not complete","request_id":"req-7f3a9c","rule_id":"R-VNFC-DOWN","rule_name":"VNFC marked down: check peer SIP gateway","status":"FAILED","error":"grule: rule timeout after 2s"}
```

Ví dụ rule bị `SKIPPED` (thiếu fact cần thiết, chạy trên context không đầy đủ):

```json
{"time":"...","msg":"rca rule did not complete","status":"SKIPPED","error":"grule: unknown attribute Vdu.Path"}
```

---

## Luồng log theo thời gian cho 1 request

### Request thành công (context đầy đủ, 1 rule match)

```
08:31:44.201  INFO  context snapshot built     (request_id, status=COMPLETE, snapshot)
08:31:44.218                                   (rule matched hoàn thành → không có WARN)
08:31:44.219  INFO  rpc completed              (rca_status=COMPLETE, root_causes=1)
```

### Request lỗi (lỗi DB)

```
08:32:01.555  ERROR analyze alert failed       (server.go — full error có DSN)
08:32:01.555  ERROR rpc failed                 (interceptor.go — code=Internal)
```

### Request PARTIAL (provider lỗi, rule vẫn chạy)

```
08:33:10.110  WARN  link provider failed       (provider=LINK, error=...)
08:33:10.111  INFO  context snapshot built     (status=PARTIAL)
08:33:10.120  WARN  rca rule did not complete  (×2: rule engine + analysis service)
08:33:10.121  INFO  rpc completed              (overall_status=PARTIAL, missing_context=N)
```

---

## Mức độ log tóm tắt

| Mức |Khi nào phát sinh |
| --- | --- |
| `INFO` | RPC thành công, snapshot build xong, startup/shutdown bình thường |
| `WARN` | Provider lỗi (có thể tiếp tục với context thiếu), rule fail/skip, grace period hết |
| `ERROR` | RPC trả error, handler trả error về client |
