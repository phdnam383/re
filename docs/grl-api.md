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
| `Ctx.Alert.HasOverload(entity string)` | `bool` | Trả về `true` khi `entity` hoặc một thực thể nằm dưới nó có ít nhất một alert `THRESHOLD_CROSSING` với metric bắt đầu bằng `overload`. |
| `Ctx.Alert.OverloadCount(entity string)` | `int` | Đếm alert overload của `entity` và các thực thể nằm dưới nó. Trả về `0` khi không có alert phù hợp. |

## `Ctx.Vdu`

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Vdu.DesiredReplicas(path string)` | `int` | Trả về số replica được khai báo của VDU. Trả về `0` nếu VDU không có trong snapshot. |
| `Ctx.Vdu.ReadyReplicas(path string)` | `int` | Đếm VNFC có trạng thái `RUNNING` thuộc VDU. |
| `Ctx.Vdu.IsDegraded(path string)` | `bool` | Trả về `true` khi VDU có `DesiredReplicas > 0` và số VNFC `RUNNING` nhỏ hơn số replica mong muốn. VDU bị thiếu hoặc chủ động scale về `0` không được coi là degraded. |

## `Ctx.Vnfc`

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Vnfc.Status(path string)` | `string` | Trả về trạng thái của VNFC. Trả về chuỗi rỗng nếu VNFC không có trong snapshot. |
| `Ctx.Vnfc.IsDown(path string)` | `bool` | Trả về `true` khi đúng VNFC được chỉ định có trạng thái `TERMINATED`. `UNKNOWN` và VNFC bị thiếu trả về `false`. |
| `Ctx.Vnfc.HasAnyDownInVDU(vduPath string)` | `bool` | Trả về `true` khi có ít nhất một VNFC thuộc VDU có trạng thái `TERMINATED`. |
| `Ctx.Vnfc.DownPathsInVDU(vduPath string)` | `[]string` | Trả về danh sách path của tất cả VNFC `TERMINATED` thuộc VDU, theo thứ tự xác định. Trả về danh sách rỗng nếu không có VNFC down hoặc VDU bị thiếu. |
| `Ctx.Vnfc.Parent(path string)` | `string` | Trả về path của VDU sở hữu VNFC theo dữ liệu snapshot. Trả về chuỗi rỗng nếu VNFC bị thiếu. |

## `Ctx.Link`

Link có hướng: kiểm tra `src -> dst` không tự động kiểm tra hoặc thay thế bằng
`dst -> src`. Với các hàm bên dưới, link `DOWN` hoặc `DEGRADED` được coi là
không sử dụng được; `UNKNOWN` không được coi là bằng chứng link down.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Link.Status(src string, dst string)` | `string` | Trả về trạng thái của đúng link `(src, dst)`. Trả về chuỗi rỗng nếu snapshot không có link này. |
| `Ctx.Link.IsDown(src string, dst string)` | `bool` | Trả về `true` khi đúng link `(src, dst)` có trạng thái `DOWN` hoặc `DEGRADED`. |
| `Ctx.Link.IsSeveredBetween(srcPath string, dstVDU string)` | `bool` | Kiểm tra kết nối từ nguồn tới VDU đích. `srcPath` có thể là một VDU nguồn hoặc một VNFC nguồn cụ thể. Trả về `true` khi có ít nhất một link phù hợp và tất cả link phù hợp đều `DOWN` hoặc `DEGRADED`; tập link rỗng trả về `false`. |
| `Ctx.Link.IsSeveredTo(srcVDU string, dstPath string)` | `bool` | Kiểm tra mọi link từ các VNFC thuộc VDU nguồn tới đúng một VNFC đích. Trả về `true` khi có ít nhất một link phù hợp và tất cả đều `DOWN` hoặc `DEGRADED`. |
| `Ctx.Link.DownCountBetween(srcPath string, dstVDU string)` | `int` | Đếm link `DOWN` hoặc `DEGRADED` từ nguồn tới VDU đích. `srcPath` có thể là VDU nguồn hoặc một VNFC nguồn cụ thể. |

## `Ctx.Configuration`

Các hàm này đọc effective configuration mà NF đang chạy, không đọc desired
configuration được khai báo trong PostgreSQL.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Configuration.Has(path string, key string)` | `bool` | Trả về `true` khi configuration provider đã đọc thành công entry `(path, key)`, kể cả khi giá trị JSON là `null`. Nên dùng làm guard trước getter. |
| `Ctx.Configuration.GetFloat(path string, key string)` | `float64` | Trả về giá trị số của entry. Trả về `0` nếu entry bị thiếu hoặc giá trị không phải số. |
| `Ctx.Configuration.GetString(path string, key string)` | `string` | Trả về giá trị chuỗi của entry. Trả về chuỗi rỗng nếu entry bị thiếu hoặc giá trị không phải chuỗi; số không được tự động chuyển thành chuỗi. |

## `Result`

`Result.Assert(...)` phải được gọi thành công trước một hàm `Recommend*` trong
cùng GRL rule đang firing. Quan hệ này được scope theo từng GRL rule, nên action
không bị gắn nhầm vào assertion của rule khác khi nhiều rule chạy trong cùng
một tài liệu.

| API | Return | Description |
| --- | --- | --- |
| `Result.Assert(category string, role string, summary string)` | `void` | Tạo hoặc chọn root cause theo khóa đầy đủ `(category, role, summary)`. `role` hợp lệ là `PRIMARY`, `CONTRIBUTING` hoặc `SUSPECTED`. Các root cause trùng đủ ba trường được merge. |
| `Result.RecommendRestartVNFC(paths []string)` | `void` | Thêm một component cho mỗi VNFC path. `entity` và `mo_instance` đều bằng path; action có `code = RESTART_VNFC`, `op = REPLACE` và không có `value`. Danh sách rỗng là lỗi rule. |
| `Result.RecommendSetConfig(entity string, moInstance string, value any)` | `void` | Thêm action cấu hình cho `entity`. Action có `code = SET_CONFIG`, `mo_instance` lấy từ tham số, `op = REPLACE`, và `value` giữ nguyên kiểu scalar/object được truyền vào. `mo_instance` phải xác định đầy đủ setting cần thay đổi. |

`Result.Err()` và `Result.RootCauses()` là API tích hợp nội bộ giữa runtime và
engine, không phải API được hỗ trợ cho tác giả GRL.

