# AtCoder／Codeforces 每日題目 Bot

Go + PostgreSQL 的 Discord Forum Bot。每日預設發布 AtCoder、Codeforces 各一題，在同一討論串呈現完整繁體中文翻譯與原始範例。題目結尾提供原站、私人提示、私人解答按鈕。

題意、輸入輸出、限制及範例會合併成完整正文；超過 Discord 單則 2,000 UTF-16 units 限制才分段，不附日期／題號／段落編號的分段標頭。必要圖片盡量併入正文訊息。

VPS 的獨立部署、Discord Token 設定與啟動步驟見 [VPS 部署手冊](docs/vps.md)。

第一版採用 spec.md 的基本方案；O-A～O-J 選配功能未納入。預設為 Asia/Taipei 09:00、AtCoder 原始估計 difficulty 400～1000、CF rating 800～1400、90 天去重。這些是可修改的專案預設，不是兩平台難度換算。

## 本機啟動

需要 Go 1.26 以上、PostgreSQL 18，以及 Discord Application。選定相容 LLM 供應商與模型後：

```sh
cp .env.example .env
cp config.example.yaml config.yaml
# 編輯 .env、config.yaml；模型由部署者選擇。
set -a
. ./.env
set +a
make build
./bin/atc-cf-bot check-config --config config.yaml
./bin/atc-cf-bot migrate
./bin/atc-cf-bot check-config --config config.yaml --connect
./bin/atc-cf-bot run --config config.yaml
```

正式 DB 連線需 `sslmode=verify-full`，並停用 `DATABASE_ALLOW_INSECURE_LOCAL`。本機例外必须明確開啟。`migrate` 只需要 DB 設定；`run` 不自行修改 schema。`check-config --connect` 讀取 DB、Discord 身分／Forum 權限及 LLM `/models`，不建立貼文或產生付費生成內容；不支援 `/models` 的相容端點需由維護者另外驗證。

設定優先順序是內建預設、指定的 YAML、環境變數。每個部署欄位都有對應的大寫環境變數，例如 `LLM_BASE_URL`、`LLM_MODEL`、`WORKER_PUBLIC_CONCURRENCY`；密鑰使用 `DISCORD_BOT_TOKEN`、`LLM_API_KEY`、`DATABASE_URL`。未知 YAML 欄位、重複 key 與明確空的環境覆寫會被拒絕。模型預設為空，必須填寫。

## Docker Compose

```sh
cp .env.example .env
cp config.example.yaml config.yaml
# 填入 token、API key、模型及本機資料庫密碼。
docker compose up --build -d
```

Compose 先等 PostgreSQL healthy，再執行一次性 `migrate` service，最後啟動 Bot。PostgreSQL 18 volume 掛在 `/var/lib/postgresql`，包含版本化的資料目錄。Bot 以非 root、唯讀檔案系統執行；原文、圖片與恢復資料都存 DB。停止使用 `docker compose down`；不要使用 `down -v`，除非要刪除資料。

Compose 的 PostgreSQL 密碼用于 URI，請使用不含 URI 保留字元的本機密碼；正式部署應透過秘密管理供應已編碼的 DSN 並啟用驗證 TLS。

## Discord 安裝與設定

1. 在 Developer Portal 建立 Application 與 Bot，設定 **Guild Install**。邀請 scope 選 `bot`、`applications.commands`。
2. 建立既有 Forum Channel。Bot 需要 View Channel、Send Messages、Send Messages in Threads、Read Message History、Embed Links、Attach Files。不需 Administrator 或 Message Content intent。
3. 邀請 Bot，啟動後由具有 Manage Server 權限的管理員設定：

```text
/config set key:forum_channel_id value:123456789012345678
/config set key:forum_tag_ids value:["234567890123456789"]
/config set key:admin_log_channel_id value:345678901234567890
/config show
/daily publish
/daily show
```

Forum 要求 tag 時必須使用該 Forum 的既有 tag ID。沒有設定 Forum 的伺服器仍可使用 help/config，不會接受發題工作。開發時可設定 `DISCORD_TEST_GUILD_ID`，正式環境留空使用全域指令。指令只在定義改變時更新。

## 指令

| 指令 | 使用者 | 行為 |
| --- | --- | --- |
| `/problem random [source] [min_difficulty] [max_difficulty]` | 一般成員／指定角色 | 當日加一題，不扣每日配額。難度覆寫需指定單一來源且在管理員區間內。 |
| `/daily show` | 一般成員 | 今日貼文連結與狀態。 |
| `/daily publish` | 管理員 | 接受或恢復當日每日工作，沿用相同貼文與名額。 |
| `/config show`、`/config set key value` | 管理員 | 查看或更新允許的設定，不回傳密鑰。 |
| `/help` | 一般成員 | 指令說明。 |

設定 key 見 [config 型別](internal/domain/domain.go) 與 [規格第 8 節](spec.md)。角色、tag 清單使用 JSON array；權重是正整數。公開設定於每日紀錄建立時凍結，既有日期的題數、來源、難度、Forum 不因設定更新而改變。時區於舊時區當日結束後生效；語言、角色及冷卻按新互動讀取最新值。

提示／題解固定私人回覆；長內容分段，過長時改用私人文字附件。題解包含思路、演算法步驟、正確性、時間與空間複雜度及範例說明，不附完整參考程式碼。AI 內容未經原站評測。

## 持久化與恢復

公開發送先保存不可變 payload，再記錄 sending，最後保存 Discord ID。程序中止或網路結果不明時先核對 Bot 作者、所在討論串、完整內容、按鈕及附件；不能證明唯一結果時阻擋該伺服器，避免盲目重送。Discord nonce 僅是短期輔助，不是跨系統 exactly-once 保證。

```sh
# 唯讀核對，可與 run 並行。
./bin/atc-cf-bot reconcile --config config.yaml
# 先停止 run；驗證並認領已存在的訊息或 Forum。
./bin/atc-cf-bot reconcile --config config.yaml \
  --operation OPERATION_UUID --adopt DISCORD_ID \
  --actor maintainer --reason '已核對原始內容與作者'
# 只有維護者確認沒有送達後，才明確允許該 operation 重送。
./bin/atc-cf-bot reconcile --config config.yaml \
  --operation OPERATION_UUID --confirm-not-delivered \
  --actor maintainer --reason '已人工完整檢查 Discord 歷史'
```

寫入 reconcile、migrate 與 run 共用專用 PostgreSQL session advisory lock。不支援 transaction pooling 的鎖連線；請直連 primary 或使用保留 session 的 proxy，並如實設定 `database.lock_connection_mode`。Singleton 失效會取消工作並關閉 Gateway。私人 interaction token 只存記憶體，重啟後重新點按鈕取得保存的結果。

備份／還原與故障處理見 [維護手冊](docs/operations.md)。

## 驗證

```sh
make check                 # vet、單元/HTTP 模擬測試、race
make integration           # 一次性 PostgreSQL 18 Docker 容器
TEST_DATABASE_URL='postgres://.../disposable_test_db?sslmode=disable' go test -race ./...
```

整合測試會清空測試資料，**只可指定專用測試 DB**。不提供 `TEST_DATABASE_URL` 時 DB 測試會明確 skip；一般測試不使用真實 LLM 額度。[驗收追蹤](docs/acceptance.md) 區分已自動驗證與仍需真實 Discord／容器環境驗證的項目。

Linux 上若 LLM 僅監聽主機 `localhost:11434`，可使用專用 Compose override，讓 Bot 使用 host network 並把 DB 僅暴露在 localhost 的 55432：

```sh
docker compose -f compose.yaml -f compose.local-llm.yaml up --build -d
```

一般容器中的 `localhost` 指向該容器；若使用其他網路部署，請設定容器能連到的 LLM base URL。

真實來源與 LLM 驗收為明確 opt-in：

```sh
LIVE_SOURCE_TEST=1 go test -run TestLiveOfficialStatements ./internal/source
# 下面會消耗供應商生成額度。
LIVE_LLM_BASE_URL=http://localhost:11434/v1 \
LIVE_LLM_MODEL=deepseek-v4.1-flash:cloud \
LIVE_LLM_API_KEY=local-placeholder \
go test -run TestLiveCompatibleLLM ./internal/generation
```
