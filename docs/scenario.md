# Kịch bản RCA

Tài liệu này mô tả bằng lời từng kịch bản mà rule set hiện có xử lý: cảnh báo
nào kích hoạt nó, context nào cần có để nó suy diễn được, và nó kết luận ra cái
gì. API mà rule gọi được mô tả riêng trong [grl-api.md](grl-api.md).

Mỗi file `.grl` là một `rca_rule` row — đơn vị được engine load, compile, cache
và fail nguyên khối. Các rule bên trong một file là đơn vị Grule fire, có
salience riêng, và trong một lần chạy có thể fire nhiều rule cùng lúc. Mỗi file
`.grl` cần một `context_profile` tương ứng: profile quyết định dữ liệu nào được
nạp vào snapshot, và rule chỉ suy diễn được trên đúng những gì profile đã xin.

## Tổng quan

| Kịch bản | Rule | Profile | Probable cause | Hành động đề xuất |
| --- | --- | --- | --- | --- |
| SIPGW Core mất kết nối | [link_to_sipgw_down.grl](../grule/link_to_sipgw_down.grl) | [link_to_sipgw_down.json](../context_profile/link_to_sipgw_down.json) | `LINK_TO_PEER_SIPGW_DOWN` | Restart VNFC |
| DIAGW Core mất kết nối | [link_to_diagw_down.grl](../grule/link_to_diagw_down.grl) | [link_to_diagw_down.json](../context_profile/link_to_diagw_down.json) | `LINK_TO_PEER_DIAGW_DOWN` | Restart VNFC |
| H248 gateway mất kết nối | [link_to_h248gw_down.grl](../grule/link_to_h248gw_down.grl) | *chưa có* | `LINK_TO_H248GW_DOWN` | Restart VNFC |
| SB logic mất kết nối | [link_to_logic_down.grl](../grule/link_to_logic_down.grl) | *chưa có* | `LINK_TO_LOGIC_DOWN` | Restart VNFC |
| SIPGW Access mất kết nối peer | [link_to_sipgw_access_down.grl](../grule/link_to_sipgw_access_down.grl) | [link_to_sipgw_access_down.json](../context_profile/link_to_sipgw_access_down.json) | `LINK_TO_PEER_SIPGW_DOWN` | Báo NOC |
| DNS server không phản hồi | [link_to_dns_server_down.grl](../grule/link_to_dns_server_down.grl) | [link_to_dns_server_down.json](../context_profile/link_to_dns_server_down.json) | `LINK_TO_DNS_SERVER_DOWN` | Báo NOC |
| Diameter peer không phản hồi | [link_to_diagw_peer_down.grl](../grule/link_to_diagw_peer_down.grl) | [link_to_diagw_peer_down.json](../context_profile/link_to_diagw_peer_down.json) | `LINK_TO_PEER_DIAGW_DOWN` | Báo NOC |
| Quá tải TPS | [tps_overloaded.grl](../grule/tps_overloaded.grl) | [tps_overloaded.json](../context_profile/tps_overloaded.json) | `THRESHOLD_CROSSING` | Restart VNFC + đổi cấu hình |
| Quá tải RAM do cấu hình log | [ram_overloaded.grl](../grule/ram_overloaded.grl) | [ram_overloaded.json](../context_profile/ram_overloaded.json) | `RAM_OVERLOAD` | Đổi cấu hình |
| Bảng nội bộ đầy | [table_size_overloaded.grl](../grule/table_size_overloaded.grl) | [table_size_overloaded.json](../context_profile/table_size_overloaded.json) | `SIPGW_*_SIZE` | Xoá bản ghi cũ nhất |
| Erlang node cạn tài nguyên | [erlang_node_overloaded.grl](../grule/erlang_node_overloaded.grl) | [erlang_node_overloaded.json](../context_profile/erlang_node_overloaded.json) | `PROCESS_COUNT_EXCEED`, `PORT_COUNT`, `ATOM_COUNT` | Restart VNFC |
| Tiến trình ứ hàng đợi | [process_queue_backlog.grl](../grule/process_queue_backlog.grl) | [process_queue_backlog.json](../context_profile/process_queue_backlog.json) | `MESSAGE_QUEUE_LEN` | Reset tiến trình hoặc restart VNFC |

Ba nhóm dưới đây khác nhau ở chỗ **ai là người sửa được lỗi**, và đó là thứ
quyết định hình dạng của kết luận. Lỗi nằm trong IMS thì engine đề xuất hệ thống
tự restart; lỗi nằm ngoài IMS thì không có managed object nào của mình để sửa,
nên việc duy nhất đúng là chuyển cho người; lỗi do cấu hình thì đề xuất chính
giá trị cần đổi.

---

## 1. Mất kết nối tới thành phần nội bộ IMS

Cùng một khuôn: một NF phía SB mất kết nối tới một thành phần trong lõi IMS.
Engine tìm xem thành phần nào phía sau đường link đó đang `TERMINATED`, rồi đề
xuất restart đúng những instance đó.

Cả nhóm dựa trên hai fact bổ sung cho nhau. `Ctx.Link.IsSeveredBetween` xác nhận
đường đi thực sự đứt — và lượng hoá bằng "mọi link đều down", không phải "có một
link down", nên một instance chập chờn trong cụm nhiều instance không bị báo
thành sự cố. `Ctx.Vnfc.HasAnyDownInVDU` xác nhận có thành phần chết thật, chứ
không phải chỉ đường truyền có vấn đề.

### 1.1 SIPGW Core mất kết nối

Cảnh báo `LINK_TO_PEER_SIPGW_DOWN` từ một VNFC của `ims.vdu_sb_sip_core`, khi
đường tới load balancer I-CSCF đã đứt hoàn toàn. Ba rule kiểm tra ba chặng của
đường báo hiệu, theo thứ tự từ ngoài vào trong.

| Salience | Rule | Thành phần nghi vấn | Kết luận |
| --- | --- | --- | --- |
| 100 | `SIPGWLoadBalancerDown` | `ims.vdu_cs_loadbalancer_icscf` | `SIPGW_DOWN` / PRIMARY |
| 90 | `SIPGWICSCFDown` | `ims.vdu_cs_sip_icscf` | `SIPGW_DOWN` / PRIMARY |
| 80 | `SIPGWLogicDown` | `ims.vdu_cs_logic` | `SIPGW_DOWN` / PRIMARY |

Ba rule không loại trừ nhau: chết nhiều tầng cùng lúc thì cả ba cùng fire và
sinh ra ba root cause cùng category, mỗi cái mang component riêng của nó. Điều
kiện link thì cả ba dùng chung — luôn là đường tới load balancer I-CSCF, kể cả
khi thành phần nghi vấn nằm sâu hơn, vì đó là đường mà VNFC phát cảnh báo thực
sự đi qua.

### 1.2 DIAGW Core mất kết nối

Cùng khuôn với 1.1, cho hướng Diameter: cảnh báo `LINK_TO_PEER_DIAGW_DOWN` từ
`ims.vdu_sb_diameter_core`, đường tới load balancer DIAGW đứt hoàn toàn. Bốn
chặng thay vì ba.

| Salience | Rule | Thành phần nghi vấn | Kết luận |
| --- | --- | --- | --- |
| 100 | `DIAGWLoadBalancerDown` | `ims.vdu_cs_loadbalancer_diagw` | `DIAGW_DOWN` / PRIMARY |
| 90 | `DIAGWDiameterRouterDown` | `ims.vdu_cs_diameter_router` | `DIAGW_DOWN` / PRIMARY |
| 80 | `DIAGWLogicDown` | `ims.vdu_cs_diag_logic` | `DIAGW_DOWN` / PRIMARY |
| 70 | `DIAGWHSSConnectorDown` | `ims.vdu_cs_hss_connector` | `DIAGW_DOWN` / PRIMARY |

### 1.3 H248 gateway và SB logic

Hai kịch bản một-rule, cùng khuôn nhưng nguồn và đích nằm cùng một VDU đích:

| Rule | Cảnh báo | VDU | Kết luận |
| --- | --- | --- | --- |
| `SBH248GWDown` | `LINK_TO_H248GW_DOWN` | `ims.vdu_sb_h248gw` | `SB_H248GW_DOWN` / PRIMARY |
| `SBLogicDown` | `LINK_TO_LOGIC_DOWN` | `ims.vdu_sb_logic` | `SB_LOGIC_DOWN` / PRIMARY |

Cả hai **chưa có context profile**. Không có profile thì không profile nào match
cảnh báo, request dừng ở `ErrContextProfileNotFound` và rule không bao giờ được
chạy — hai file này hiện là rule chết.

---

## 2. Mất kết nối tới đối tác ngoại vi

Khác nhóm 1 ở chỗ đối tác nằm ngoài IMS: thiết bị đầu cuối, mạng truy cập, DNS
server, hay Diameter peer của nhà mạng khác. Không có VNFC nào của mình để
restart, nên chuỗi suy diễn không hỏi "thành phần nào chết" mà hỏi "hỏng ở lớp
nào" hoặc "server nào hỏng", và kết luận luôn đi kèm một thông báo cho người.

Nhóm này dùng `Ctx.Probe` — kết quả ping và SIP OPTIONS tại thời điểm build
context — bên cạnh `Ctx.Link`. Hai nguồn bằng chứng này trả lời cùng một câu hỏi
theo hai cách: link status là thứ topology đã ghi nhận và cũng chính là thứ sinh
ra cảnh báo, còn probe là phép thử ngay lúc phân tích. Tách được hai thứ đó là
điều kiện để phân biệt "peer thực sự chết" với "cảnh báo cũ còn treo".

Mỗi kịch bản đều có một rule cuối cùng cho trường hợp mọi phép thử đều thành
công. Nó không kết luận sự cố mà đưa ra một finding `SUSPECTED`: cảnh báo có vẻ
chập chờn hoặc đã cũ, nhờ NOC xác nhận trước khi clear.

### 2.1 SIPGW Access mất kết nối peer

Cảnh báo `SBC_SIP_GATEWAY_ACCESS_LINK_STATUS` với probable cause
`LINK_TO_PEER_SIPGW_DOWN`, phát ra từ VM SIP GW Access khi mất kết nối hướng
Access. Đối tác là `remote.sipa_peer` — nằm ngoài lõi IMS — nên nguyên nhân
không đến từ I-CSCF hay S-CSCF mà từ đường truyền hoặc báo hiệu phía đối tác.

Suy diễn theo hai lớp, đúng thứ tự một kỹ sư sẽ làm bằng tay: ping trước, nếu IP
thông mới gửi SIP OPTIONS.

| Salience | Rule | Ping | OPTIONS | Kết luận |
| --- | --- | --- | --- | --- |
| 100 | `SIPGWAccessIPLinkDown` | thất bại | — | `SIPGW_ACCESS_IP_LINK_DOWN` / PRIMARY — báo đội truyền dẫn kiểm tra đường vật lý và định tuyến |
| 90 | `SIPGWAccessSIPLinkDown` | thành công | thất bại | `SIPGW_ACCESS_SIP_LINK_DOWN` / PRIMARY — đề nghị đối tác kiểm tra cấu hình SIP và port đầu xa |
| 80 | `SIPGWAccessPeerServiceUp` | thành công | thành công | `SIPGW_ACCESS_LINK_FLAPPING` / SUSPECTED — nhờ NOC xác nhận cảnh báo đã cũ |

Ba nhánh loại trừ nhau theo kết quả probe nên mỗi lần chạy chỉ một rule fire.
Hai root cause đầu tách category chứ không chỉ tách summary, vì chúng dẫn tới
hai đội xử lý khác nhau — consumer lọc theo category phải phân biệt được.

### 2.2 DNS server không phản hồi

Cảnh báo `LINK_TO_DNS_SERVER_DOWN` từ NF đang dùng resolver. Câu hỏi cần trả lời
là **server nào** trong cụm `remote.dns` đã chết, nên mỗi server một rule.

| Salience | Rule | Server | Kết luận |
| --- | --- | --- | --- |
| 100 | `DNSServerPrimaryDown` | `remote.dns.srv_1` | `DNS_SERVER_DOWN` / PRIMARY |
| 90 | `DNSServerSecondaryDown` | `remote.dns.srv_2` | `DNS_SERVER_DOWN` / PRIMARY |
| 80 | `DNSServersReachable` | cả hai đều trả lời ping | `DNS_SERVER_LINK_FLAPPING` / SUSPECTED |

Ở đây "server nào" **không** được trả lời bằng category — hai server hỏng là
cùng một loại lỗi ở hai instance khác nhau — mà bằng `entity` của component, tức
path của chính server đó. Hỏng cả hai thì hai rule cùng fire và sinh ra hai
finding, mỗi cái nêu tên server của nó.

Điều kiện link dùng `Ctx.Link.IsSeveredTo`, lượng hoá là "mọi instance của VDU
phát cảnh báo đều không tới được đúng server này". Nếu chỉ dùng link của riêng
VM phát cảnh báo thì rất có thể lỗi nằm ở VM đó chứ không phải ở DNS server.

VDU nguồn không hardcode mà lấy từ `Ctx.Vnfc.Parent(Ctx.Alert.SourcePath())`,
nên rule chạy được với bất kỳ NF nào dùng resolver — miễn là VDU đó có trong
profile.

### 2.3 Diameter peer không phản hồi

Cùng khuôn với 2.2, cho cụm `remote.dia`.

| Salience | Rule | Server | Kết luận |
| --- | --- | --- | --- |
| 100 | `DIAServerPrimaryDown` | `remote.dia.srv_1` | `DIA_SERVER_DOWN` / PRIMARY |
| 90 | `DIAServerSecondaryDown` | `remote.dia.srv_2` | `DIA_SERVER_DOWN` / PRIMARY |
| 80 | `DIAServersReachable` | cả hai đều trả lời ping | `DIA_SERVER_LINK_FLAPPING` / SUSPECTED |

Kịch bản này dùng chung probable cause `LINK_TO_PEER_DIAGW_DOWN` với 1.2, nên
hai bộ rule phải được tách ở tầng profile — xem [Ranh giới giữa các
profile](#ranh-giới-giữa-các-profile).

---

## 3. Quá tải do cấu hình

Nhóm này không hỏi "cái gì chết" mà hỏi "cấu hình nào sai", và đề xuất chính giá
trị cần đổi qua `Result.RecommendSetConfig`.

### 3.1 Quá tải TPS

Cảnh báo `THRESHOLD_CROSSING` với `metric = overload_ram` trên
`ims.vdu_sb_logic`. Hai rule nhìn hai khía cạnh khác nhau của cùng một sự cố.

| Salience | Rule | Điều kiện | Kết luận |
| --- | --- | --- | --- |
| 100 | `TPSVNFCDown` | có VNFC `TERMINATED` trong `ims.vdu_sb_logic` | `TPS_OVERLOADED` / PRIMARY — restart các instance chết |
| 90 | `TPSHighLogFileConfiguration` | `number_of_log_file >= 3` | `HIGH_LOG_FILE_CONFIG` / CONTRIBUTING — hạ về `3` |

Rule thứ hai mang role CONTRIBUTING chứ không PRIMARY: cấu hình log nhiều làm
tình hình tệ hơn nhưng không phải thứ khởi phát quá tải. Hai rule độc lập nhau
nên có thể cùng fire, cho ra một finding PRIMARY và một CONTRIBUTING.

### 3.2 Quá tải RAM do cấu hình log

Cảnh báo `RAM_OVERLOAD`. Câu hỏi là **setting nào** đang ăn RAM, đối chiếu với
hạn mức bộ nhớ của chính instance đó.

| Salience | Rule | Điều kiện | Kết luận |
| --- | --- | --- | --- |
| 100 | `RAMOverloadLogFileCount` | log chiếm ≥ 20% hạn mức **và** `cfg_log_file_count >= 10` | `HIGH_LOG_FILE_COUNT_CONFIG` / PRIMARY — hạ về `5` |
| 90 | `RAMOverloadLogFileSize` | log chiếm ≥ 20% hạn mức **và** `cfg_log_file_size >= 100` MB | `HIGH_LOG_FILE_SIZE_CONFIG` / PRIMARY — hạ về `50` |
| 80 | `RAMOverloadVerboseLogLevel` | `cfg_log_level` là `DEBUG` hoặc `TRACE` | `VERBOSE_LOG_LEVEL_CONFIG` / CONTRIBUTING — đặt về `INFO` |

Ở đây "cấu hình nào" **được** trả lời bằng category riêng cho từng setting —
ngược với 2.2 — vì ba setting là ba loại lỗi khác nhau và mỗi cái sửa ở một chỗ
khác nhau. Sai nhiều setting cùng lúc thì nhiều rule cùng fire, mỗi rule kèm
`SET_CONFIG` của riêng nó.

`limit_memory` là thước đo chứ không phải thủ phạm: nó chỉ vào công thức
`Ctx.Cfg.LogFootprintRatio`, và không rule nào đề xuất nâng hạn mức. Log level
mang role CONTRIBUTING vì `DEBUG`/`TRACE` không tự chiếm RAM — thứ chiếm RAM là
buffer do count × size quy định — nó chỉ làm khối lượng log phình ra.

---

## 4. Bảng dữ liệu nội bộ đầy

Một họ cảnh báo riêng, mỗi cái báo một bảng trong bộ nhớ NF đã chạm giới hạn
kích thước: `SIPGW_TRANSACTION_GARBAGE_TIMER_SIZE`,
`SIPGW_TCP_CONNS_MANAGEMENT_TB_TMP_SIZE`, và các bảng khác cùng dạng. Không có
thành phần nào chết và không có cấu hình nào sai — bảng chỉ đơn giản là đầy, và
việc cần làm là dọn bớt bản ghi cũ nhất.

Câu hỏi "bảng nào" được trả lời ngay bởi probable cause: mỗi bảng có cảnh báo
riêng của nó, nên mỗi bảng là một rule.

| Salience | Rule | Cảnh báo | Bảng |
| --- | --- | --- | --- |
| 100 | `SIPGWTransactionGarbageTimerTableFull` | `SIPGW_TRANSACTION_GARBAGE_TIMER_SIZE` | `transaction_garbage_timer` |
| 90 | `SIPGWTCPConnsManagementTableFull` | `SIPGW_TCP_CONNS_MANAGEMENT_TB_TMP_SIZE` | `tcp_conns_management_tb_tmp` |

Cả hai cùng kết luận `TABLE_SIZE_OVERLOAD` / PRIMARY và đề xuất
`PURGE_OLDEST_ROWS`. Dùng chung một category vì đây là cùng một loại lỗi ở hai
bảng khác nhau — giống cách hai DNS server dùng chung `DNS_SERVER_DOWN`, và
ngược với 3.2 nơi ba setting log tách thành ba category vì mỗi cái là một loại
lỗi cấu hình khác nhau. Bảng cụ thể nằm ở `mo_instance` của action.

Rule fire khi bảng đạt 90% giới hạn, nhưng xoá đủ để về 80%. Hai mốc khác nhau
là cố ý: xoá đúng phần vượt 90% sẽ đưa bảng về đúng ngưỡng cảnh báo, và bản ghi
tiếp theo kích hoạt lại cảnh báo ngay lập tức.

Thêm một bảng vào họ này là thêm một rule và hai entry configuration trong
profile. GRL không có vòng lặp nên không có cách nào viết một rule chạy cho mọi
bảng; đổi lại, mỗi bảng có ngưỡng riêng và mức dọn riêng nếu cần.

---

## 5. Erlang node cạn tài nguyên

Ba cảnh báo cho ba hạn mức của Erlang VM mà NF đang chạy trên đó. Khác nhóm 3 ở
chỗ không có setting nào sai để sửa, và khác nhóm 4 ở chỗ không có dữ liệu nào
để dọn: hạn mức là tham số khởi động của VM, nên cách duy nhất đưa node về trạng
thái sạch là restart chính instance đó.

| Salience | Rule | Cảnh báo | Tài nguyên |
| --- | --- | --- | --- |
| 100 | `ErlangProcessCountExceeded` | `PROCESS_COUNT_EXCEED` | process — node không spawn được process mới |
| 90 | `ErlangPortCountExceeded` | `PORT_COUNT` | port — node không mở được socket mới |
| 80 | `ErlangAtomCountExceeded` | `ATOM_COUNT` | atom table |

Cả ba cùng kết luận `ERLANG_RESOURCE_EXHAUSTED` / PRIMARY với summary nêu rõ tài
nguyên nào, và cùng đề xuất restart đúng instance phát cảnh báo. Cạn nhiều tài
nguyên cùng lúc thì nhiều rule fire và sinh ra nhiều finding cùng trỏ về một
hành động — chúng không xung đột vì mỗi finding là một root cause riêng, mỗi cái
mang component của chính nó.

Riêng atom có lý do kỹ thuật đứng sau: atom trong Erlang VM không bao giờ được
thu hồi, nên hết atom table thì không có cách nào giải phóng ngoài restart. Với
process và port thì restart là cách nhanh nhất, nhưng nếu cảnh báo lặp lại đều
đặn thì nguyên nhân thật nằm ở rò rỉ process/port trong ứng dụng hoặc ở hạn mức
đặt quá thấp — cả hai đều nằm ngoài phạm vi rule set hiện tại.

Điều kiện không chỉ dựa vào cảnh báo mà còn kiểm tra tỉ lệ sử dụng thực tế ≥ 90%.
Cảnh báo nói rằng node *đã từng* chạm ngưỡng; tỉ lệ nói rằng nó *đang* chạm. Một
cảnh báo còn treo từ đợt trước sẽ không làm engine đề xuất restart một node đã
tự hồi phục.

Đây là kịch bản duy nhất dùng `Result.RecommendRestartVNFCAt`. Các kịch bản ở
nhóm 1 restart *những instance đang TERMINATED trong một VDU* nên nhận cả danh
sách; ở đây instance cần restart là chính cái phát ra cảnh báo, đã biết đích
danh, nên nhận đúng một path.

---

## 6. Tiến trình ứ hàng đợi

Cảnh báo `MESSAGE_QUEUE_LEN`: một tiến trình Erlang nhận bản tin nhanh hơn tốc độ
nó xử lý, hàng đợi dồn lại. Cùng một cảnh báo nhưng cách xử lý phụ thuộc vào
tiến trình nào đang ứ, nên kịch bản chia làm hai nhánh.

| Salience | Rule | Tiến trình | Kết luận |
| --- | --- | --- | --- |
| 100 | `TCPConnWorkerQueueBacklog` | `tcp_conn_worker` | `PROCESS_QUEUE_BACKLOG` / PRIMARY — reset riêng tiến trình đó |
| 90 | `OtherProcessQueueBacklog` | mọi tiến trình khác | `PROCESS_QUEUE_BACKLOG` / PRIMARY — restart cả VNFC |

`tcp_conn_worker` có nhánh riêng vì nó reset lại được một mình mà không kéo theo
phần còn lại của node. Tiến trình khác thì không có đường phục hồi cục bộ nào
được biết, nên biện pháp duy nhất là restart node — đắt hơn hẳn, và đó là lý do
hai nhánh phải tách nhau chứ không gộp thành một kết luận chung.

Nhánh thứ hai **không** hardcode tên tiến trình. Các nhóm trước đều liệt kê sẵn
đối tượng nghi vấn: mỗi VDU một rule, mỗi bảng một rule, mỗi tài nguyên một
rule. Ở đây danh sách tiến trình là mở, nên liệt kê trước đồng nghĩa với việc
một tiến trình lạ ứ hàng đợi mà không rule nào bắt được. Thay vào đó nhánh này
hỏi context "còn tiến trình nào khác đang vượt ngưỡng" và trả lời bằng chính
danh sách đó.

Vì vậy "tiến trình nào" không nằm trong summary mà nằm ở component. Ở nhánh thứ
hai, mỗi tiến trình ứ hàng đợi là một component riêng, nhưng mọi component đều
mang cùng một action `RESTART_VNFC` trỏ vào node: `entity` trả lời "ai gây ra",
`mo_instance` trả lời "sửa ở đâu", và hai câu hỏi đó ở đây có hai câu trả lời
khác nhau.

Tham số `except` là thứ giữ hai nhánh không chồng lấn. `tcp_conn_worker` bị loại
khỏi nhánh tổng quát, nếu không nó sẽ vừa nhận đề xuất reset vừa nhận đề xuất
restart node cho cùng một sự cố. Hai nhánh vẫn có thể cùng fire khi cả
`tcp_conn_worker` lẫn một tiến trình khác cùng ứ — khi đó hai finding là đúng,
dù restart node trên thực tế đã bao gồm cả việc reset worker.

Ngưỡng `1000` xuất hiện hai lần ở nhánh thứ hai và **phải giống nhau**, cùng với
`except`. `HasBacklogExcept` quyết định rule có fire không,
`BackloggedNamesExcept` quyết định quy trách nhiệm cho tiến trình nào; lệch nhau
sẽ khiến rule fire vì một tiến trình rồi kết luận về một tập tiến trình khác.

Ngưỡng là số tuyệt đối chứ không phải tỉ lệ như nhóm 4 và 5, vì mailbox của tiến
trình Erlang không có hạn mức cấu hình — nó lớn đến khi node hết bộ nhớ, nên
không có mẫu số nào để chia.

---

## Quy ước chung

**Salience là thứ tự đọc, không phải độ ưu tiên loại trừ.** Grule fire mọi rule
match được trong một pass; salience chỉ quyết định rule nào chạy trước. Rule
loại trừ nhau là do điều kiện của chúng loại trừ nhau (như ba nhánh probe ở
2.1), không phải do salience.

**Thiếu dữ liệu thì rule im lặng, không đoán.** Mọi fact đều trả về `false` hoặc
zero value khi snapshot không có dữ liệu, và mọi điều kiện đều viết theo chiều
khẳng định. Hệ quả: profile khai thiếu một target không làm engine kết luận sai,
nhưng làm nó không kết luận gì cả — và đó là lý do phần lớn lỗi vận hành của rule
set nằm ở profile chứ không nằm ở rule.

**Một root cause được định danh bằng đủ `(category, role, summary)`.** Hai rule
assert trùng cả ba trường sẽ được merge thành một finding và gộp component. Chỉ
định đích danh một instance thì dùng `entity` của component; phân biệt hai loại
lỗi khác nhau thì tách `category`.

**Rule không có action là hợp lệ.** Engine chỉ đề xuất thứ nó thực sự đứng sau
được.

## Ranh giới giữa các profile

Hai cặp kịch bản dùng chung probable cause và chỉ tách nhau được ở selector:

| Cặp | Phân biệt bằng |
| --- | --- |
| SIPGW Core (1.1) ↔ SIPGW Access (2.1) | Core đòi `dst_path` có mặt; Access đòi `link_side = "ACCESS"` |
| DIAGW Core (1.2) ↔ Diameter peer (2.3) | Core đòi `dst_path` có mặt; peer đòi `link_side = "PEER"` |

Ranh giới này chỉ đứng vững khi cảnh báo hướng ngoại vi **không** mang
`dst_path`. Nếu nó có, cả hai profile cùng match, snapshot được merge, và hai bộ
rule cùng chạy — cho ra đồng thời "restart thành phần nội bộ" và "báo truyền
dẫn", tức hai kết luận trái ngược nhau về hướng xử lý.

`link_side` là trường do tài liệu này giả định, chưa được xác nhận với IFM/OAM.

## Phụ thuộc chưa implement

Bảy trong mười hai file rule gọi API chưa có trong `internal/ruleengine`. Row nào
gọi API thiếu sẽ FAILED ngay ở bước compile hoặc evaluate và toàn bộ output của
row đó bị loại bỏ.

| Kịch bản | Phụ thuộc |
| --- | --- |
| 2.1, 2.2, 2.3 | `Ctx.Probe.*`, `Result.RecommendNotifyNOC` |
| 3.2 | `Ctx.Cfg.*` |
| 4 | `Ctx.Table.*`, `Result.RecommendPurgeOldestRows` |
| 5 | `Ctx.Erl.*`, `Result.RecommendRestartVNFCAt` |
| 6 | `Ctx.Proc.*`, `Result.RecommendResetProcess`, `Result.RecommendRestartVNFCFor` |

Danh sách đầy đủ và hợp đồng của từng hàm nằm ở mục [Trạng thái
implement](grl-api.md#trạng-thái-implement) trong `grl-api.md`.

Ba kịch bản chạy được ngay hôm nay là 1.1, 1.2 và 3.1. Hai kịch bản 1.3 chạy
được về mặt API nhưng thiếu context profile.
