# 維護與故障處理

## 備份與還原

備份必須包含全部 schema、原文、圖片與進度，不可只備份 guild settings。

```sh
docker compose exec -T postgres pg_dump -U atc_bot -d atc_bot -Fc > backup.dump
# 停止 Bot，再將備份還原到獨立 DB／volume；先驗證還原內容。
docker compose stop bot
docker compose exec -T postgres pg_restore -U atc_bot -d atc_bot --clean --if-exists < backup.dump
# 覆蓋 command，使用恢復模式；不接受／排程新公開工作。
docker compose run --rm bot run --config /etc/bot/config.yaml --recovery-mode
```

舊備份不代表 Discord 已回滾。恢復模式只核對保存的 sending/ambiguous operation，維護者還必須檢查備份之後建立、DB 中沒有紀錄的孤立討論串與訊息。不要自動建立新的每日貼文。人工確認所有既有效果、必要時採用既有訊息或終止孤立工作後，停止恢復模式，以一般 `run` 重新啟動。解除模式是維護者明確的重新啟動決定。

## 常見狀態

| 狀態／問題 | 處理 |
| --- | --- |
| configuration / schema 不符 | 檢查必要欄位；停止舊 Bot、先備份、執行 migrate。不要刪庫修復。 |
| needs_reconciliation | 先執行唯讀 reconcile；唯一結果可核對認領。找不到或扫描受限都不證明未送達。 |
| Forum 被刪除／鎖定 | 修復權限或鎖定狀態後，管理員用 daily publish 恢復；已刪除貼文不自動重建。 |
| no_candidate | 檢查來源快照、難度、去重與 parser 拒絕理由；不自動放寬条件。修正後 daily publish。 |
| LLM 400／401／403 | 修正部署參數／密鑰並重新啟動；本次程序會停用錯誤的模型／端點。 |
| 429／網路／5xx | 私人生成至多兩次重試，共用總 deadline。公開 5xx 先核對，明確 429 才依 Retry-After 再送。 |
| 供應商明確回報上下文過長 | 結構化 `context_length_exceeded` 不停用整個模型。翻譯在既有總期限內縮小 block；提示／解答保留完整原文，超出模型限制便失敗，不改成摘要。 |
| 舊日 incomplete | 保留 confirmed 内容，標記 expired；不補過去多日。 |
| 第二個 run 起不來 | 檢查是否已有 Bot／migration／寫入 reconcile 持有同一 singleton；不要繞過鎖。 |
| admin 通知失敗 | 查看 JSON 部署日誌的安全錯誤碼；不反覆嘗試通知。 |

`GET /livez` 是程序回應檢查；`GET /readyz` 檢查初始化、Gateway 與 DB。題庫或 LLM 錯誤顯示 degraded，不應據此反覆重啟。健康端點預設只綁 localhost，Compose 綁容器內網且未發布外部 port。

日誌不輸出來源全文、供應商 body、Token、DSN 或 interaction URL。需要補充調查時，只記錄 operation/job ID、stage、安全錯誤碼；不要啟用會把 Authorization dump 到日誌的 HTTP debug。

正常關機中斷且沒有不明確外部效果時，job 重排為 queued，保留已確認段落、凍結 payload 與發布順序，不消耗自動重試次數。下一次啟動沿用同一 job；若已跨日則過期，不補發。結果不明確的發送即使跨午夜仍需先核對。

Forum 自動核對及人工認領都核對 Discord ID 中的建立時間，允許範圍為保存的 `send_started_at` 前 2 分鐘至後 10 分鐘。這個範圍不隨重啟延長；缺少發送時間或落在範圍外時維持未解決狀態，不能直接當作未送達重建。

## 真實測試伺服器驗收

維護者配置密鑰與測試 guild 後，在專用 Forum 執行：

1. `check-config --connect`；確認 Bot 邀請、scope、角色與必要 tag。
2. `daily publish`，等待完整兩題、來源按鈕、原始全部範例及總覽連結。
3. 長題、多範例及必要圖片；检查每段與原站內容。
4. 兩個成員同時點同題提示／解答，確認各自 ephemeral 且生成合併。
5. 重啟後點舊按鈕；驗證原文版本與舊 publication 綁定。
6. 撤銷 queued manual 發起者角色，確認執行前拒絕；偽造其他 guild／thread／message 的按鈕不可讀取。
7. 發送期間中止程序，再啟動；確認唯一已發內容才認領，剩餘段落沿用原 payload。
8. 刪除／鎖定 Bot 貼文與撤銷權限，確認停止且無重建。
9. Compose 容器重建與完整備份還原；在 recovery mode 檢查孤立内容後再解除。

這些操作會公開寫入測試 Discord，提示／解答會消耗真實 LLM 額度。一般單元測試使用模擬服務。
