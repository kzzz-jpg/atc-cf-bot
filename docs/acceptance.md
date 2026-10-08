# 驗收追蹤

此檔逐項對應 spec.md v0.2。已在目前環境通過：`go vet ./...`、全專案單元／HTTP 測試、真實 PostgreSQL 18.0 整合測試與 `go test -race ./...`。此外，使用者指定的 `http://localhost:11434/v1`／`deepseek-v4.1-flash:cloud` 已完成真實翻譯、提示與 C++17 解答測試；兩平台官方題目頁的真實抓取／解析也已通過。

自動測試使用固定 HTML／JSON、模擬 HTTP 與真實 PostgreSQL 18，不消耗真實 LLM 額度。測試通過只證明所列範圍；真實 Discord 與容器驗收沒有密鑰／Docker daemon 時不能宣稱已通過。

| 驗收 | 實作／自動證據 | 尚需的外部驗證 |
| --- | --- | --- |
| A01、A02 | Scheduler、來源配額測試、PostgreSQL 完整發題測試 | 真實 Discord 排程觸發；兩平台題目頁存取已驗證。 |
| A03、A20、A42 | 平台 parser 固定樣本、原始 sample 保留、placeholder multiset、數學轉換測試 | 翻譯語意与必要圖片人工核對。 |
| A04、A41 | UTF-16、grapheme、超長 sample、Markdown fence、公式不拆分測試 | 真實 Discord 長訊息顯示。 |
| A05 | 每題末段三按鈕 payload 測試 | 真實 URL 按鈕開啟。 |
| A06 | 先 ephemeral defer 再非同步路由；私人分段／附件 | 真實 3 秒 ACK、ephemeral 與附件顯示。 |
| A07、A38、A39 | Coordinator 共享 context、一次生成、等待者上限、公私公平佇列測試 | 多成員實際互動。 |
| A08、A09、A13 | PostgreSQL 手動先發／每日補齊、同日同貼文、重啟不重送測試 | 真實 Forum 建立。 |
| A10、A11、A36 | 設定驗證／樂觀版本測試、REST 每次重新讀取角色；HTTP 測試驗證撤銷成員／管理角色後拒絕、thread 繼承 parent 與 overwrite 優先順序 | 角色／權限撤銷的真實 Discord 情境。 |
| A12、A19、A40 | LLM httptest 請求格式、限制重試、停用錯誤設定、finish/refusal/HTML/JSON 驗證；明確上下文過長時縮小翻譯 block，保留全文且不停用模型 | 已驗證指定 localhost 供應商；其他端點仍需單獨驗證。 |
| A14、A15 | 不可變 payload、confirmed 段落、sending 恢復、丟失回應核對；PostgreSQL＋模擬 publisher 測試 | 真實 Discord crash point。 |
| A16、A21 | DB 按鈕查詢綁 guild/thread/confirmed message；完整發題後重啟按鈕與跨 guild 拒絕測試 | 真實重啟後按鈕互動。 |
| A17、A34、A35 | UTC DB clock、當日 snapshot、DST、pending timezone、deadline／expiry、expired lease 恢復；PostgreSQL 測試驗證到期前不建立每日紀錄、手動先建立後仍依凍結時間排程 | 真實午夜／時區切換與排程觀察。 |
| A18、A37 | 整批 transaction 切換題庫、difficulty NULL 與負值、保留時重新驗證；無候選不放寬 | 大型真實題庫同步與失敗沿用。 |
| A22 | 安全 Error code、CLI／slog 不印底層 body／DSN、HTTP secrets 不進快取 | 部署日誌抽查。 |
| A23、A43 | Fingerprint 包含 immutable statement ID、內容／parser／prompt／模型／endpoint／參數；API key 不影響 fingerprint | 原文／模型更新的實際生成核對。 |
| A24 | Compose PostgreSQL 18 volume、immutable statement/assets、持久化 jobs/operations | Docker 容器重建，尚需 daemon。 |
| A25、A26 | 專用 pgx session lock；PostgreSQL 第二 singleton 拒絕；配置拒絕 transaction 模式 | 實際 proxy 模式核對。 |
| A27、A28、A29 | PostgreSQL 同 guild claim 互斥、lease epoch、DB 時間過期、取消 rollback／pool reuse 測試 | 多 guild 壓力／DB 故障演練。 |
| A30、A31 | 限 20 頁、Bot 作者／內容／按鈕／附件核對；Forum 建立時段與缺失證據測試；找不到、多份或掃描未完成皆停止 | 真實 active/archived Forum 查詢與多吻合測試。 |
| A32 | REST adapter httptest 證明 create 的 5xx／丟失回應只有一個 request；429 顯式交回；bucket wait 可取消 | 真實 rate-limit bucket 路由。 |
| A33 | 長期 interaction job identity；receipt 刪除後重送不配置第二名額；Router bounded dedup | 真實 Gateway redelivery。 |
| A44 | URL／redirect／DNS IP allowlist、bounded read、圖片 MIME/像素驗證，獨立 client 無 Authorization | 真實官方圖片 host 範圍與特殊格式。 |
| A45 | 每次公開操作前 Forum／thread／permissions 檢查；locked/deleted 停止；overview expected-before 衝突；HTTP 測試驗證必要 tag 與撤銷附件權限後拒絕 | 人工修改／刪除／鎖定的真實情境。 |
| A46 | 顯式 goose migration、版本檢查、共用 singleton、immutable schema constraints 整合測試；失敗 migration 不留下部分 schema／成功版本且保留既有資料；原生 PostgreSQL 18 全庫備份還原資料指紋一致 | migration 失敗／備份恢復的容器演練。 |
| A47 | 真實 PostgreSQL 終止 singleton backend 後取消 application root，後續可重新取鎖；worker 心跳失敗取消、關機中斷重排且保留 frozen 進度、不消耗自動重試；午夜未知發送仍需核對 | 完整 Bot 的 SIGTERM、DB 斷線與 Gateway 關閉演練。 |
| A48 | `--recovery-mode` 禁止新公開接受／scheduler，維護核對與人工解除流程 | 完整備份落後 Discord 的還原演練；孤立效果需維護者核對。 |

自動測試入口是 `go test ./...` 與 `go test -race ./...`；真實 DB 測試需專用 `TEST_DATABASE_URL`。`scripts/test-postgres.sh` 為同 major PostgreSQL 容器測試入口。[維護手冊](operations.md) 列出真實測試伺服器的逐步驗收。

第一版尚未宣稱跨系統 exactly-once、翻譯語意完全正確、解答 AC 或多副本高可用。題目必要公式／圖片／長度不能完整處理時排除，來源 outage 不記為永久題目拒絕。

補充目前環境證據：已抓取並離線保存 ABC100 A 與 CF 4/A 的官方 HTML。ABC100 A 的兩張必要 PNG 圖片已成功下載並通过實際解碼、MIME 與像素上限檢查。離線回歸測試檢查題名、範例数量、必要圖片数量及可讀公式；不需要連線。

另以 PostgreSQL 18 的 `pg_dump -Fc` 與 `pg_restore --exit-on-error`，將測試資料庫完整還原到另一個獨立資料庫；public 與 posting_test 共 38 張表的列數及逐列資料指紋一致。此項驗證包含已保存的原文、生成、payload 與進度，不包含 Discord 外部狀態或 Docker volume 還原。
