# GRL Rule API

Tài liệu này mô tả API được hỗ trợ khi viết rule GRL. `Ctx` chỉ đọc dữ liệu
từ context snapshot; `Result` ghi kết luận và hành động đề xuất của rule.

Các giá trị trạng thái và probable cause được so sánh không phân biệt chữ hoa,
chữ thường. Khi dữ liệu không có trong snapshot, các getter trả về zero value
được ghi rõ bên dưới; rule nên dùng hàm `Has*` tương ứng khi cần phân biệt dữ
liệu bị thiếu với một giá trị zero hợp lệ.

## `Ctx.Alert`

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Alert.HasCause(cause string)` | `bool` | Trả về `true` khi alert có `probable_cause` bằng `cause`. So sánh không phân biệt chữ hoa, chữ thường. |
| `Ctx.Alert.SourcePath()` | `string` | Trả về managed-object path của VNFC phát ra alert. Trả về chuỗi rỗng nếu request không có alert. |
| `Ctx.Alert.VduPath()` | `string` | Trả về path của VDU sở hữu source path phát ra alert. Resolve cho cả source path dạng VNFC (trả về VDU cha) và dạng VDU (trả về chính nó). Trả về chuỗi rỗng nếu request không có alert hoặc source path không có trong snapshot (thiếu cả VDU lẫn VNFC tương ứng). |

## `Ctx.Vdu`

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Vdu.DesiredReplicas(path string)` | `int` | Trả về số instance được khai báo của VDU (trường `instances`). Trả về `0` nếu VDU không có trong snapshot. |
| `Ctx.Vdu.ReadyReplicas(path string)` | `int` | Đếm VNFC có trạng thái `RUNNING` thuộc VDU. |
| `Ctx.Vdu.IsDegraded(path string)` | `bool` | Trả về `true` khi VDU có `DesiredReplicas > 0` và số VNFC `RUNNING` nhỏ hơn số instance mong muốn. VDU bị thiếu hoặc chủ động scale về `0` không được coi là degraded. |

## `Ctx.Vnfc`

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Vnfc.Status(path string)` | `string` | Trả về trạng thái của VNFC. Trả về chuỗi rỗng nếu VNFC không có trong snapshot. |
| `Ctx.Vnfc.IsDown(path string)` | `bool` | Trả về `true` khi đúng VNFC được chỉ định có trạng thái `TERMINATED`. `UNKNOWN` và VNFC bị thiếu trả về `false`. |
| `Ctx.Vnfc.HasAnyDownInVDU(vduPath string)` | `bool` | Trả về `true` khi có ít nhất một VNFC thuộc VDU có trạng thái `TERMINATED`. |
| `Ctx.Vnfc.DownPathsInVDU(vduPath string)` | `[]string` | Trả về danh sách path của tất cả VNFC `TERMINATED` thuộc VDU, theo thứ tự xác định. Trả về danh sách rỗng nếu không có VNFC down hoặc VDU bị thiếu. |
| `Ctx.Vnfc.Parent(path string)` | `string` | Trả về path của VDU sở hữu VNFC theo dữ liệu snapshot. Trả về chuỗi rỗng nếu VNFC bị thiếu. |

## `Ctx.Link`

Kết quả các phép kiểm tra chủ động lên một peer, hiện là phép ping ở lớp IP. Tham
số luôn là path của *đối tượng được kiểm tra*, không phải của thực thể thực hiện
phép kiểm tra. `Ctx.Link` đọc kết quả kiểm tra ngay tại thời điểm build context,
không phải trạng thái lưu sẵn.

Mỗi phép kiểm tra có hai hàm ngược nhau, và rule **không được** coi hàm này là
phủ định của hàm kia: khi probe không có trong snapshot thì cả hai đều trả về
`false`. Đây là cùng một nguyên tắc với `Ctx.Vnfc.IsDown` — không biết peer có
trả lời hay không thì không phải là bằng chứng rằng nó không trả lời, và một rule
kết luận trên cơ sở đó là đang kết luận trên lỗ hổng dữ liệu của chính mình.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Link.PingToRemoteFailed()` | `bool` | Dùng target từ `alert.additional_information.remote_ip`; trả về `true` khi probe tự động đã chạy và remote không phản hồi. |
| `Ctx.Link.PingToRemoteSuccess()` | `bool` | Dùng target từ `alert.additional_information.remote_ip`; trả về `true` khi probe tự động đã chạy và remote có phản hồi. |
| `Ctx.Link.PingFails(path string)` | `bool` | Trả về `true` khi phép ping tới `path` đã được thực hiện và không có phản hồi. Probe không có trong snapshot trả về `false`. |
| `Ctx.Link.PingAnswers(path string)` | `bool` | Trả về `true` khi ping tới `path` có phản hồi. Probe không có trong snapshot trả về `false`. |

Nguồn dữ liệu là Link Provider, đọc từ API probe của NF thực hiện phép kiểm
tra, với status như `"OK"` | `"UNREACHABLE"`.

Có hai nguồn tạo Link Provider work:

- Profile khai từng probe bằng `providers.link[].target` để dùng với
  `PingFails(path)`/`PingAnswers(path)`.
- Auto-enrichment đọc `remote_ip` dạng chuỗi, khác rỗng từ
  `additional_information` của alert đầu tiên. Target này được merge và
  deduplicate với target từ profile, rồi dùng với hai API `PingToRemote*`.

Auto-enrichment chỉ bổ sung plan sau khi đã có ít nhất một profile match; nó
không thay thế yêu cầu context profile. Nếu `remote_ip` thiếu/sai kiểu, hoặc
probe không có trong snapshot, cả hai API `PingToRemote*` đều trả về `false`.

Địa chỉ API probe (`http://api/v1/probe/ping`) là hằng số của provider, không
khai lại trong profile. Provider gửi `POST` tới endpoint đó với body JSON:

```json
{
  "vnfcPath": "<alert.source_path>",
  "target": "10.55.70.37",
  "count": 3,
  "timeoutMs": <probe timeout, milliseconds>
}
```

`vnfcPath` là `source_path` của alert gốc (alert đầu trong incident). `count` cố định bằng `3` (engine không tune số lần probe). `timeoutMs` là timeout cho một lần ping; deadline của toàn bộ HTTP request là `count * timeoutMs + 500ms` để đủ thời gian chạy các lần ping và encode response. Response nhận về là một JSON object có trường `status`, giá trị của trường này là trạng thái ping.

## `Ctx.Cfg`

Các hàm này đọc effective configuration mà NF đang chạy, không đọc desired
configuration được khai báo trong PostgreSQL.

Profile khai báo `"configuration": ["log_file_count", "log_level", ...]`.
Mỗi key được gắn với `alert.source_path` đầy đủ. Khi dựng URL, provider lấy
hai cấp đầu của path, ví dụ `ims.vdu_sb_logic.vnfc_sb_logic_1` →
`GET <base>/ims.vdu_sb_logic.<key>`. Snapshot chỉ lưu key, value và thời điểm
đọc; các hàm `Ctx.Cfg` chỉ trả dữ liệu khi path truyền vào khớp source path
của alert đang được phân tích.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Cfg.Has(path string, key string)` | `bool` | Trả về `true` khi configuration provider đã đọc thành công entry `(path, key)`, kể cả khi giá trị JSON là `null`. Nên dùng làm guard trước getter. |
| `Ctx.Cfg.GetFloat(path string, key string)` | `float64` | Trả về giá trị số của entry. Trả về `0` nếu entry bị thiếu hoặc giá trị không phải số. |
| `Ctx.Cfg.GetString(path string, key string)` | `string` | Trả về giá trị chuỗi của entry. Trả về chuỗi rỗng nếu entry bị thiếu hoặc giá trị không phải chuỗi; số không được tự động chuyển thành chuỗi. |
| `Ctx.Cfg.LogFileCount(path string)` | `float64` | Số file log NF giữ lại (`log_file_count`). Trả về `0` nếu entry bị thiếu. |
| `Ctx.Cfg.LogFileSizeMB(path string)` | `float64` | Kích thước tối đa mỗi file log, tính bằng MB (`log_file_size`). Trả về `0` nếu entry bị thiếu. |
| `Ctx.Cfg.LogLevel(path string)` | `string` | Trả về giá trị raw của `log_level`. Trả về chuỗi rỗng nếu entry bị thiếu. |
| `Ctx.Cfg.IsLogVerbose(path string)` | `bool` | Trả về `true` khi `log_level` là `DEBUG` hoặc `TRACE`. So sánh không phân biệt chữ hoa, chữ thường; entry bị thiếu trả về `false`. |
| `Ctx.Cfg.MemoryLimitMB(path string)` | `float64` | Hạn mức bộ nhớ của instance, tính bằng MB (`limit_memory`). Trả về `0` nếu entry bị thiếu. |
| `Ctx.Cfg.LogFootprintRatio(path string)` | `float64` | Phần hạn mức bộ nhớ mà log có thể chiếm: `LogFileCount * LogFileSizeMB / MemoryLimitMB`. Trả về `0` khi thiếu bất kỳ đầu vào nào hoặc khi hạn mức bằng `0`. |

## `Ctx.Erl`

Các bộ đếm tài nguyên của Erlang VM mà NF đang chạy trên đó. Cả ba hàm trả về
mức sử dụng so với hạn mức của chính node, chứ không trả về số tuyệt đối: hạn
mức là tham số khởi động của VM và khác nhau giữa các node, nên một con số đếm
trần không nói lên điều gì nếu không đặt cạnh hạn mức của node đó.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Erl.ProcessRatio(path string)` | `float64` | Tỉ lệ process đang dùng trên hạn mức process của node. Trả về `0` khi thiếu dữ liệu hoặc hạn mức bằng `0`. |
| `Ctx.Erl.PortRatio(path string)` | `float64` | Tỉ lệ port đang dùng trên hạn mức port. Trả về `0` khi thiếu dữ liệu hoặc hạn mức bằng `0`. |
| `Ctx.Erl.AtomRatio(path string)` | `float64` | Tỉ lệ atom đã tạo trên hạn mức atom table. Trả về `0` khi thiếu dữ liệu hoặc hạn mức bằng `0`. |

Nguồn dữ liệu là field addtion_information trong Alert, với các cảnh báo dạng này sẽ đi kèm addtional_information có cấu trúc:

```json
{
    "additional_information" : {
        "metric"         : "process",
        "observed_value" : 90,
        "threshold_value": 80
    }
}

```

## `Ctx.Table`

Kích thước các bảng dữ liệu nội bộ mà NF đang giữ trong bộ nhớ — bảng transaction
timer, bảng quản lý kết nối TCP, và những bảng tương tự. Tham số `table` là tên
bảng theo NF, không phải một path.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Table.FillRatio(path string, table string)` | `float64` | Phần giới hạn kích thước mà bảng đang chiếm: số bản ghi hiện có chia cho số bản ghi tối đa. Trả về `0` khi thiếu bất kỳ đầu vào nào hoặc khi giới hạn bằng `0`. |
| `Ctx.Table.RowsAbove(path string, table string, ratio float64)` | `int` | Số bản ghi vượt quá `ratio` lần giới hạn — tức số bản ghi phải xoá để đưa bảng về đúng mức đó. Trả về `0` khi thiếu dữ liệu, khi giới hạn bằng `0`, hoặc khi bảng chưa vượt mức. |

`RowsAbove` nhận `ratio` thay vì tự chọn mức an toàn, vì "xoá về bao nhiêu" là
chính sách vận hành và chính sách thuộc về rule. Ngược lại phép chia thì nằm
trong fact, cùng lý do với `Ctx.Cfg.LogFootprintRatio`: mẫu số bằng `0` phải
được xử lý một lần ở một chỗ, chứ không phải ở mọi rule.

Hai hàm này cố tình dùng hai mốc khác nhau. Rule cảnh báo khi bảng chạm mốc trên
(`FillRatio >= 0.9`) nhưng xoá về mốc dưới (`RowsAbove(..., 0.8)`). Nếu chỉ xoá
đúng phần vượt mốc trên, bảng quay lại đúng ngưỡng cảnh báo và bản ghi tiếp theo
sẽ kích hoạt lại cảnh báo ngay.

Nguồn dữ liệu là field addtion_information trong Alert, với các cảnh báo dạng này sẽ đi kèm addtional_information có cấu trúc:

```json
{
    "additional_information" : {
        "metric"         : "process",
        "observed_value" : 90,
        "threshold_value": 80
    }
}
```

Field metric sẽ đại diện cho tên bảng dữ liệu vượt quá size dẫn đến cảnh báo.

## `Ctx.Metric`

Giá trị số của một performance metric, đọc trực tiếp tại thời điểm build context
— không phải dữ liệu có sẵn trong alert. Đây là nguồn dữ liệu khác với
`Ctx.Erl` và `Ctx.Table`: hai nhóm kia đọc cặp `observed_value`/`threshold_value`
theo kèm alert (đã có sẵn), còn `Ctx.Metric` gọi Metric Provider để lấy giá trị
hiện tại của một metric bất kỳ mà profile khai thu thập.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Metric.Value(name string)` | `float64` | Trả về giá trị số của metric có `name`. Trả về `0` khi entry thiếu hoặc giá trị không phải số (chuỗi, boolean, object). |
| `Ctx.Metric.AbsValue(name string)` | `float64` | Trả về trị tuyệt đối của `Value(name)`. Trả về `0` khi entry thiếu hoặc giá trị không phải số. |

Nguồn dữ liệu là Metric Provider: một lần GET tới
`http://metrics/{vduPath}.{name}?vnfc_path=<source_path>` cho mỗi `name`,
trong đó `vduPath` là phần path của source path trừ nhãn VNFC cuối cùng. Response
là một JSON object có trường `value`; các trường khác (như `path`, `unit`) bị bỏ qua.

```json
{
  "value": 42
}
```

Context profile khai danh sách metric cần thu thập trong `providers.metric` —
mỗi entry là một chuỗi `name`:

```json
{
  "providers": {
    "metric": [
      "pm_process_count",
      "pm_atom_count"
    ]
  }
}
```

## `Result`

`Result.Assert(...)` phải được gọi thành công trước một hàm `Recommend*` trong
cùng GRL rule đang firing. Quan hệ này được scope theo từng GRL rule, nên action
không bị gắn nhầm vào assertion của rule khác khi nhiều rule chạy trong cùng
một tài liệu.

| API | Return | Description |
| --- | --- | --- |
| `Result.Assert(category string, role string, summary string)` | `void` | Tạo hoặc chọn root cause theo khóa đầy đủ `(category, role, summary)`. `role` hợp lệ là `PRIMARY`, `CONTRIBUTING` hoặc `SUSPECTED`. Các root cause trùng đủ ba trường được merge. |
| `Result.RecommendRestartVNFC(paths []string)` | `void` | Thêm một component cho mỗi VNFC path. `entity` và `mo_instance` đều bằng path; action có `code = RESTART_VNFC`, `op = REPLACE` và không có `value`. Danh sách rỗng là lỗi rule. |
| `Result.RecommendRestartVNFCAt(path string)` | `void` | Dạng một-instance của `RecommendRestartVNFC`: thêm đúng một component với `entity` và `mo_instance` bằng `path`, `code = RESTART_VNFC`, `op = REPLACE`, không có `value`. Dành cho rule đã biết chính xác instance cần restart — điển hình là instance phát ra alert. `path` rỗng là lỗi rule. |
| `Result.RecommendSetConfig(entity string, moInstance string, value any)` | `void` | Thêm action cấu hình cho `entity`. Action có `code = SET_CONFIG`, `mo_instance` lấy từ tham số, `op = REPLACE`, và `value` giữ nguyên kiểu scalar/object được truyền vào. `mo_instance` phải xác định đầy đủ setting cần thay đổi. |
| `Result.RecommendPurgeOldestRows(entity string, table string, rows int)` | `void` |  Thêm action dọn bảng cho `entity`. Action có `code = PURGE_OLDEST_ROWS`, `mo_instance` bằng `entity + "_" + table`, `op = REMOVE`, và `value` là số bản ghi cũ nhất cần xoá. `rows` nhỏ hơn hoặc bằng `0` là lỗi rule — một action xoá `0` bản ghi không phải là đề xuất, nó là dấu hiệu rule đã fire trên dữ liệu nó không có. |
| `Result.RecommendNotifyNOC(entity string, message string)` | `void` | Thêm một component chuyển việc cho NOC. `entity` và `mo_instance` đều bằng managed-object path mà thông báo nói về; action có `code = NOTIFY_NOC`, `op = NOTIFY` và `value` là `message`. `message` phải nêu rõ việc cần làm và bộ phận cần liên hệ, vì đây là toàn bộ nội dung operator nhận được. `entity` rỗng hoặc `message` rỗng là lỗi rule. |

`Result.Err()` và `Result.RootCauses()` là API tích hợp nội bộ giữa runtime và
engine, không phải API được hỗ trợ cho tác giả GRL.
