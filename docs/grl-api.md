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
| `Ctx.Vdu.Exists(path string)` | `bool` | **Chưa được implement.** Trả về `true` khi snapshot có VDU tại `path`. Dùng làm guard khi rule cần phân biệt "VDU không tồn tại trong context" với "VDU tồn tại nhưng khai báo 0 replica" — hai trường hợp mà `DesiredReplicas` đều trả về `0`. Xem [Trạng thái implement](#trạng-thái-implement). |
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

## `Ctx.Probe`

**Chưa được implement.** Xem [Trạng thái implement](#trạng-thái-implement).

Kết quả các phép kiểm tra chủ động lên một peer: ping ở lớp IP và bản tin SIP
OPTIONS ở lớp báo hiệu. Tham số luôn là path của *đối tượng được kiểm tra*, không
phải của thực thể thực hiện phép kiểm tra.

Nhóm này khác `Ctx.Link` ở loại bằng chứng, không phải ở câu hỏi. `Ctx.Link` đọc
trạng thái link mà topology đã ghi nhận — có thể đã cũ, và chính nó là thứ sinh
ra cảnh báo. `Ctx.Probe` đọc kết quả kiểm tra tại thời điểm build context. Rule
tách được "link báo down" khỏi "peer thực sự không trả lời" là nhờ hai nhóm này
tồn tại riêng.

Mỗi phép kiểm tra có hai hàm ngược nhau, và rule **không được** coi hàm này là
phủ định của hàm kia: khi probe không có trong snapshot thì cả hai đều trả về
`false`. Đây là cùng một nguyên tắc với `Ctx.Vnfc.IsDown` — không biết peer có
trả lời hay không thì không phải là bằng chứng rằng nó không trả lời, và một rule
kết luận trên cơ sở đó là đang kết luận trên lỗ hổng dữ liệu của chính mình.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Probe.PingFails(path string)` | `bool` | Trả về `true` khi phép ping tới `path` đã được thực hiện và không có phản hồi. Probe không có trong snapshot trả về `false`. |
| `Ctx.Probe.PingAnswers(path string)` | `bool` | Trả về `true` khi ping tới `path` có phản hồi. Probe không có trong snapshot trả về `false`. |
| `Ctx.Probe.SIPOptionsFails(path string)` | `bool` | Trả về `true` khi `path` không trả lời bản tin SIP OPTIONS hoặc trả về mã lỗi. Probe không có trong snapshot trả về `false`. |
| `Ctx.Probe.SIPOptionsAnswers(path string)` | `bool` | Trả về `true` khi `path` trả lời SIP OPTIONS bằng `200 OK`. Probe không có trong snapshot trả về `false`. |

Nguồn dữ liệu là Configuration Provider, đọc từ API của NF thực hiện phép kiểm
tra, với key quy ước `icmp_state` và `sip_options_state` và từ vựng giá trị
`"OK"` | `"FAIL"`. Context profile vẫn khai chúng như `configuration` target
bình thường; `Ctx.Probe` chỉ giấu chi tiết đó khỏi rule, vì tên key và từ vựng
giá trị là thứ có thể đổi mà không ảnh hưởng tới điều rule đang suy diễn.

## `Ctx.Configuration`

Các hàm này đọc effective configuration mà NF đang chạy, không đọc desired
configuration được khai báo trong PostgreSQL.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Configuration.Has(path string, key string)` | `bool` | Trả về `true` khi configuration provider đã đọc thành công entry `(path, key)`, kể cả khi giá trị JSON là `null`. Nên dùng làm guard trước getter. |
| `Ctx.Configuration.GetFloat(path string, key string)` | `float64` | Trả về giá trị số của entry. Trả về `0` nếu entry bị thiếu hoặc giá trị không phải số. |
| `Ctx.Configuration.GetString(path string, key string)` | `string` | Trả về giá trị chuỗi của entry. Trả về chuỗi rỗng nếu entry bị thiếu hoặc giá trị không phải chuỗi; số không được tự động chuyển thành chuỗi. |

## `Ctx.Cfg`

**Chưa được implement.** Xem [Trạng thái implement](#trạng-thái-implement).

Đọc cùng dữ liệu với `Ctx.Configuration` — effective configuration của NF —
nhưng gọi thẳng tên từng setting thay vì nhận key dưới dạng chuỗi. Rule vì thế
nói về *thứ được cấu hình*, không nói về *cách lấy giá trị đó ra*: tên key và
đơn vị của nó có thể đổi mà không ảnh hưởng tới điều rule đang suy diễn.

Mọi getter trả về `0` (hoặc `false`) khi entry không đọc được, nên không cần
guard `Has` trước khi gọi. Đánh đổi là rule không phân biệt được "chưa đọc được"
với "được cấu hình bằng 0" — chấp nhận được vì mọi ngưỡng ở đây đều so sánh theo
chiều lớn hơn, nên dữ liệu thiếu làm rule im lặng chứ không làm nó kết luận sai.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Cfg.LogFileCount(path string)` | `float64` | Số file log NF giữ lại (`cfg_log_file_count`). Trả về `0` nếu entry bị thiếu. |
| `Ctx.Cfg.LogFileSizeMB(path string)` | `float64` | Kích thước tối đa mỗi file log, tính bằng MB (`cfg_log_file_size`). Trả về `0` nếu entry bị thiếu. |
| `Ctx.Cfg.IsLogVerbose(path string)` | `bool` | Trả về `true` khi `cfg_log_level` là `DEBUG` hoặc `TRACE`. So sánh không phân biệt chữ hoa, chữ thường; entry bị thiếu trả về `false`. |
| `Ctx.Cfg.MemoryLimitMB(path string)` | `float64` | Hạn mức bộ nhớ của instance, tính bằng MB (`limit_memory`). Trả về `0` nếu entry bị thiếu. |
| `Ctx.Cfg.LogFootprintRatio(path string)` | `float64` | Phần hạn mức bộ nhớ mà log có thể chiếm: `LogFileCount * LogFileSizeMB / MemoryLimitMB`. Trả về `0` khi thiếu bất kỳ đầu vào nào hoặc khi hạn mức bằng `0`. |

`LogFootprintRatio` tồn tại như một hàm riêng chứ không để rule tự nhân chia, vì
phép so sánh viết thẳng ra sẽ sai khi dữ liệu thiếu: `footprint >= limit * 0.2`
với `limit = 0` cho `0 >= 0`, tức là `true` — rule sẽ kết luận log chiếm hết bộ
nhớ đúng vào lúc nó không đọc được hạn mức. Gộp cả phép chia vào một fact là chỗ
duy nhất xử lý được mẫu số bằng `0` một lần cho mọi rule.

## `Ctx.Erl`

**Chưa được implement.** Xem [Trạng thái implement](#trạng-thái-implement).

Các bộ đếm tài nguyên của Erlang VM mà NF đang chạy trên đó. Cả ba hàm trả về
mức sử dụng so với hạn mức của chính node, chứ không trả về số tuyệt đối: hạn
mức là tham số khởi động của VM và khác nhau giữa các node, nên một con số đếm
trần không nói lên điều gì nếu không đặt cạnh hạn mức của node đó.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Erl.ProcessRatio(path string)` | `float64` | Tỉ lệ process đang dùng trên hạn mức process của node. Trả về `0` khi thiếu dữ liệu hoặc hạn mức bằng `0`. |
| `Ctx.Erl.PortRatio(path string)` | `float64` | Tỉ lệ port đang dùng trên hạn mức port. Trả về `0` khi thiếu dữ liệu hoặc hạn mức bằng `0`. |
| `Ctx.Erl.AtomRatio(path string)` | `float64` | Tỉ lệ atom đã tạo trên hạn mức atom table. Trả về `0` khi thiếu dữ liệu hoặc hạn mức bằng `0`. |

Nguồn dữ liệu là Configuration Provider, với key quy ước `process_count` /
`process_limit`, `port_count` / `port_limit`, `atom_count` / `atom_limit`.

## `Ctx.Proc`

**Chưa được implement.** Xem [Trạng thái implement](#trạng-thái-implement).

Độ dài hàng đợi bản tin của từng tiến trình Erlang trên một node. Khác `Ctx.Erl`
ở mức quan sát: `Ctx.Erl` nói về cả VM, `Ctx.Proc` nói về từng tiến trình bên
trong nó.

| API | Return | Description |
| --- | --- | --- |
| `Ctx.Proc.QueueLen(path string, process string)` | `int` | Độ dài hàng đợi của đúng tiến trình được chỉ định. Trả về `0` khi không có dữ liệu về tiến trình đó. |
| `Ctx.Proc.HasBacklogExcept(path string, threshold int, except string)` | `bool` | Trả về `true` khi có ít nhất một tiến trình **không phải** `except` có hàng đợi dài từ `threshold` bản tin trở lên. Không có dữ liệu trả về `false`. |
| `Ctx.Proc.BackloggedNamesExcept(path string, threshold int, except string)` | `[]string` | Tên mọi tiến trình không phải `except` có hàng đợi từ `threshold` trở lên, sắp xếp xác định. Trả về danh sách rỗng khi không có tiến trình nào vượt ngưỡng hoặc khi không có dữ liệu. |

`QueueLen` dành cho tiến trình mà rule đã biết đích danh; hai hàm `Except` dành
cho phần còn lại, tức những tiến trình mà rule không liệt kê trước được. Tham số
`except` tồn tại vì đó là cách duy nhất để hai loại rule đó phủ kín tập tiến
trình mà không chồng lấn: một tiến trình đã có rule riêng thì phải bị loại khỏi
nhánh tổng quát, nếu không nó sẽ nhận hai đề xuất xử lý khác nhau cho cùng một
sự cố.

`HasBacklogExcept` và `BackloggedNamesExcept` đi thành cặp vì GRL không có
`len()`: rule cần một biểu thức `bool` cho `when` và một danh sách cho `then`.
Đây là cùng khuôn với `HasAnyDownInVDU` / `DownPathsInVDU`, và cũng mang cùng
nghĩa vụ — rule phải truyền **cùng `threshold` và cùng `except`** cho cả hai,
nếu không điều kiện và hành động sẽ nói về hai tập tiến trình khác nhau.

Ngưỡng ở đây là số tuyệt đối chứ không phải tỉ lệ như `Ctx.Erl` hay `Ctx.Table`,
vì mailbox của tiến trình Erlang không có hạn mức cấu hình: nó lớn đến khi node
hết bộ nhớ. Không có mẫu số nào để chia.

Nguồn dữ liệu là Configuration Provider, key quy ước `message_queue_lens`, giá
trị là một object JSON ánh xạ tên tiến trình sang độ dài hàng đợi. Một entry cho
cả node, không phải một entry cho mỗi tiến trình — đó là điều cho phép rule bắt
được cả tiến trình mà profile không biết trước tên.

## `Ctx.Table`

**Chưa được implement.** Xem [Trạng thái implement](#trạng-thái-implement).

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

Nguồn dữ liệu là Configuration Provider, với key quy ước `<table>_rows` và
`<table>_max_rows`.

## `Result`

`Result.Assert(...)` phải được gọi thành công trước một hàm `Recommend*` trong
cùng GRL rule đang firing. Quan hệ này được scope theo từng GRL rule, nên action
không bị gắn nhầm vào assertion của rule khác khi nhiều rule chạy trong cùng
một tài liệu.

Chiều ngược lại không bắt buộc: một rule có thể chỉ gọi `Assert` mà không gọi
`Recommend*` nào. Root cause khi đó không có component. Engine chỉ đề xuất
những gì nó thực sự đứng sau được; bịa ra một action để root cause trông "đầy
đủ" hơn sẽ gửi operator đi sửa nhầm chỗ.

`RecommendRestartVNFC` và `RecommendSetConfig` là hành động engine đề xuất *hệ
thống* thực hiện. `RecommendNotifyNOC` thì khác: nó không thay đổi gì trong
IMS, mà chuyển việc cho con người. Nó dành cho nguyên nhân nằm ngoài tầm với
của engine — đứt truyền dẫn, cấu hình của đối tác ngoại vi — nơi hành động đúng
duy nhất là báo cho người có thẩm quyền xử lý, chứ không phải sửa một managed
object nào của mình.

| API | Return | Description |
| --- | --- | --- |
| `Result.Assert(category string, role string, summary string)` | `void` | Tạo hoặc chọn root cause theo khóa đầy đủ `(category, role, summary)`. `role` hợp lệ là `PRIMARY`, `CONTRIBUTING` hoặc `SUSPECTED`. Các root cause trùng đủ ba trường được merge. |
| `Result.RecommendRestartVNFC(paths []string)` | `void` | Thêm một component cho mỗi VNFC path. `entity` và `mo_instance` đều bằng path; action có `code = RESTART_VNFC`, `op = REPLACE` và không có `value`. Danh sách rỗng là lỗi rule. |
| `Result.RecommendRestartVNFCAt(path string)` | `void` | **Chưa được implement.** Dạng một-instance của `RecommendRestartVNFC`: thêm đúng một component với `entity` và `mo_instance` bằng `path`, `code = RESTART_VNFC`, `op = REPLACE`, không có `value`. Dành cho rule đã biết chính xác instance cần restart — điển hình là instance phát ra alert. `path` rỗng là lỗi rule. Xem [Trạng thái implement](#trạng-thái-implement). |
| `Result.RecommendSetConfig(entity string, moInstance string, value any)` | `void` | Thêm action cấu hình cho `entity`. Action có `code = SET_CONFIG`, `mo_instance` lấy từ tham số, `op = REPLACE`, và `value` giữ nguyên kiểu scalar/object được truyền vào. `mo_instance` phải xác định đầy đủ setting cần thay đổi. |
| `Result.RecommendResetProcess(path string, process string)` | `void` | **Chưa được implement.** Thêm một component để reset đúng một tiến trình. `entity` và `mo_instance` đều bằng `path + "_" + process`; action có `code = RESET_PROCESS`, `op = REPLACE` và không có `value`. Tên tiến trình phải nằm trong `entity` chứ không chỉ trong `mo_instance`: hai component cùng `entity` mà khác action bị coi là xung đột và làm rule lỗi. `path` hoặc `process` rỗng là lỗi rule. Xem [Trạng thái implement](#trạng-thái-implement). |
| `Result.RecommendRestartVNFCFor(path string, processes []string)` | `void` | **Chưa được implement.** Đề xuất restart VNFC, quy trách nhiệm cho những tiến trình đã gây ra kết luận. Thêm một component cho mỗi tiến trình với `entity = path + "_" + process`, và mọi component mang cùng một action: `code = RESTART_VNFC`, `mo_instance = path`, `op = REPLACE`, không có `value`. Danh sách rỗng là lỗi rule. Xem [Trạng thái implement](#trạng-thái-implement). |
| `Result.RecommendPurgeOldestRows(entity string, table string, rows int)` | `void` | **Chưa được implement.** Thêm action dọn bảng cho `entity`. Action có `code = PURGE_OLDEST_ROWS`, `mo_instance` bằng `entity + "_" + table`, `op = REMOVE`, và `value` là số bản ghi cũ nhất cần xoá. `rows` nhỏ hơn hoặc bằng `0` là lỗi rule — một action xoá `0` bản ghi không phải là đề xuất, nó là dấu hiệu rule đã fire trên dữ liệu nó không có. Xem [Trạng thái implement](#trạng-thái-implement). |
| `Result.RecommendNotifyNOC(entity string, message string)` | `void` | **Chưa được implement.** Thêm một component chuyển việc cho NOC. `entity` và `mo_instance` đều bằng managed-object path mà thông báo nói về; action có `code = NOTIFY_NOC`, `op = ADD` và `value` là `message`. `message` phải nêu rõ việc cần làm và bộ phận cần liên hệ, vì đây là toàn bộ nội dung operator nhận được. `entity` rỗng hoặc `message` rỗng là lỗi rule. Xem [Trạng thái implement](#trạng-thái-implement). |

`Result.Err()` và `Result.RootCauses()` là API tích hợp nội bộ giữa runtime và
engine, không phải API được hỗ trợ cho tác giả GRL.

## Trạng thái implement

Các API dưới đây đã được thống nhất trong tài liệu nhưng chưa có trong
`internal/ruleengine/facts.go`. Rule dùng chúng sẽ FAILED ở bước compile hoặc
evaluate, và toàn bộ output của `rca_rule` row đó bị loại bỏ.

| API | Được dùng bởi | Trạng thái |
| --- | --- | --- |
| `Ctx.Vdu.Exists(path string)` | Chưa rule nào dùng | Chưa implement |
| `Ctx.Probe.PingFails(path string)` | `grule/link_to_sipgw_access_down.grl`, `grule/link_to_dns_server_down.grl`, `grule/link_to_diagw_peer_down.grl` | Chưa implement |
| `Ctx.Probe.PingAnswers(path string)` | `grule/link_to_sipgw_access_down.grl`, `grule/link_to_dns_server_down.grl`, `grule/link_to_diagw_peer_down.grl` | Chưa implement |
| `Ctx.Probe.SIPOptionsFails(path string)` | `grule/link_to_sipgw_access_down.grl` | Chưa implement |
| `Ctx.Probe.SIPOptionsAnswers(path string)` | `grule/link_to_sipgw_access_down.grl` | Chưa implement |
| `Ctx.Cfg.LogFileCount(path string)` | `grule/ram_overloaded.grl` | Chưa implement |
| `Ctx.Cfg.LogFileSizeMB(path string)` | `grule/ram_overloaded.grl` | Chưa implement |
| `Ctx.Cfg.IsLogVerbose(path string)` | `grule/ram_overloaded.grl` | Chưa implement |
| `Ctx.Cfg.MemoryLimitMB(path string)` | Chưa rule nào gọi trực tiếp; là đầu vào của `LogFootprintRatio` | Chưa implement |
| `Ctx.Cfg.LogFootprintRatio(path string)` | `grule/ram_overloaded.grl` | Chưa implement |
| `Ctx.Erl.ProcessRatio(path string)` | `grule/erlang_node_overloaded.grl` | Chưa implement |
| `Ctx.Erl.PortRatio(path string)` | `grule/erlang_node_overloaded.grl` | Chưa implement |
| `Ctx.Erl.AtomRatio(path string)` | `grule/erlang_node_overloaded.grl` | Chưa implement |
| `Ctx.Proc.QueueLen(path string, process string)` | `grule/process_queue_backlog.grl` | Chưa implement |
| `Ctx.Proc.HasBacklogExcept(path string, threshold int, except string)` | `grule/process_queue_backlog.grl` | Chưa implement |
| `Ctx.Proc.BackloggedNamesExcept(path string, threshold int, except string)` | `grule/process_queue_backlog.grl` | Chưa implement |
| `Ctx.Table.FillRatio(path string, table string)` | `grule/table_size_overloaded.grl` | Chưa implement |
| `Ctx.Table.RowsAbove(path string, table string, ratio float64)` | `grule/table_size_overloaded.grl` | Chưa implement |
| `Result.RecommendNotifyNOC(entity string, message string)` | `grule/link_to_sipgw_access_down.grl`, `grule/link_to_dns_server_down.grl`, `grule/link_to_diagw_peer_down.grl` | Chưa implement |
| `Result.RecommendPurgeOldestRows(entity string, table string, rows int)` | `grule/table_size_overloaded.grl` | Chưa implement |
| `Result.RecommendRestartVNFCAt(path string)` | `grule/erlang_node_overloaded.grl` | Chưa implement |
| `Result.RecommendResetProcess(path string, process string)` | `grule/process_queue_backlog.grl` | Chưa implement |
| `Result.RecommendRestartVNFCFor(path string, processes []string)` | `grule/process_queue_backlog.grl` | Chưa implement |

`NOTIFY_NOC` cũng là action code mới. `op = ADD` là chỗ duy nhất trong từ vựng
`ADD | REMOVE | REPLACE` diễn tả đúng việc này: thông báo được *thêm vào* hàng
đợi của NOC, không thay thế trạng thái nào đang có.

