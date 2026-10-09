# VPS 部署與設定

部署主機：`kzzz@192.168.68.58`。目錄：`/home/kzzz/apps/atc-cf-bot`。

使用 `scripts/vps-compose.sh` 固定 Compose 專案名稱為 `atc-cf-bot`，載入
`compose.yaml` 與 `compose.vps.yaml`。Bot 與 PostgreSQL 使用專用 Docker
網路、專用 volume，不發布主機 port。容器設有 CPU、記憶體與日誌大小限制。
不需新增 Cloudflare Tunnel；Discord Gateway 使用對外連線。

## 填入 Discord Token

1. 到 [Discord Developer Portal](https://discord.com/developers/applications)
   建立或選擇 Application。在 **Bot** 頁取得 Bot Token；如需產生新 Token，
   使用 **Reset Token**，原本 Token 會失效。
2. 在 **Installation** 啟用 **Guild Install**，設定 `bot` 與
   `applications.commands` scopes，使用安裝連結邀請到你的伺服器。
3. 建立或選擇 Forum Channel，給 Bot 以下權限：View Channel、Send Messages、
   Send Messages in Threads、Read Message History、Embed Links、Attach Files。
   本程式不需要 Message Content intent。
4. SSH 到主機並編輯私有 `.env`：

   ```sh
   ssh kzzz@192.168.68.58
   cd /home/kzzz/apps/atc-cf-bot
   nano .env
   ```

   只把 `DISCORD_BOT_TOKEN=` 後面填入實際 Token。不要加入 `Bot ` 前綴。
   已生成的 `POSTGRES_PASSWORD` 要保留，現有資料庫使用這組密碼。

已設定以下 LLM 參數：

```dotenv
LLM_BASE_URL=http://host.docker.internal:11434/v1
LLM_API_KEY=ollama-local
LLM_MODEL=deepseek-v4.1-flash:cloud
LLM_TOKEN_LIMIT_PARAMETER=max_tokens
```

`ollama-local` 是本機相容 API 所需的非空佔位值。
`host.docker.internal` 在本專案容器內指向 VPS 主機。
此模型的生成由 VPS Ollama 轉送到 Ollama Cloud，需沿用主機 Ollama 的登入狀態。

## 檢查並啟動

資料庫與 migration 已在部署時初始化。填完 Token 並邀請 Bot 後：

```sh
cd /home/kzzz/apps/atc-cf-bot
./scripts/vps-compose.sh run --rm --no-deps --pull never bot \
  check-config --config /etc/bot/config.yaml --connect
# 上面的檢查成功後才啟動。
./scripts/vps-compose.sh up --no-build -d
./scripts/vps-compose.sh ps -a
./scripts/vps-compose.sh logs --tail=50 bot
```

連線檢查會驗證資料庫、Discord 身分與 LLM 模型清單，不生成題目。
首次尚未設定 Forum，因此要在 Discord 再設定頻道。
啟動時先同步題庫；查看啟動日誌，等待 `bot running`。

在 Discord 開啟 Developer Mode，右鍵 Forum Channel 複製 Channel ID。
以具有 Manage Server 權限的帳號執行：

```text
/config set key:forum_channel_id value:你的Forum頻道ID
/config show
/help
```

Forum 若要求 tag，再設定既有 tag ID：

```text
/config set key:forum_tag_ids value:["你的TagID"]
```

這台 VPS 已設定每日台北時間 08:00 發布 AtCoder、Codeforces 各一題；
AtCoder 難度 1500～2100，Codeforces rating 1800～2400。
「給我題解」提供繁體中文思路、演算法、正確性、複雜度與範例說明，不附完整程式碼。
提示與題解使用 `||…||` 遮蔽，點擊後才顯示；分段及超長附件也會標示為 spoiler。
題目文字與範例會合併排版，超過 Discord 單則訊息上限才分段，不添加段落編號標頭。
要立即發布今日題目可執行 `/daily publish`；這會在 Forum 貼文並使用模型生成額度。

## 維護

```sh
cd /home/kzzz/apps/atc-cf-bot
./scripts/vps-compose.sh logs -f --tail=100 bot
./scripts/vps-compose.sh stop bot
# 修改 .env 後，用 up 重新建立 bot，才會載入新環境變數。
./scripts/vps-compose.sh up --no-build -d bot
# 備份本專案資料庫，備份檔只有自己可讀。
umask 077
./scripts/vps-compose.sh exec -T postgres \
  pg_dump -U atc_bot -d atc_bot -Fc > "backup-$(date +%Y%m%d-%H%M%S).dump"
```

Docker 已設為開機啟動；Bot 與 DB 使用 `restart: unless-stopped`。
若手動停止容器，需以 `up` 再啟動。
`down -v` 會刪除本專案資料庫，正常維護不要使用。

更新映像時先在開發機建置並傳送，避免占用 VPS 的編譯資源：

```sh
docker build -t atc-cf-bot:vps .
docker save atc-cf-bot:vps | gzip | ssh kzzz@192.168.68.58 'gzip -d | docker load'
```

若變更 Compose、config 或 script，需另外傳送對應檔案；不要覆蓋 `.env`。
在 VPS 先備份、停止 bot，再執行 migration，最後啟動新映像：

```sh
./scripts/vps-compose.sh stop bot
./scripts/vps-compose.sh run --rm --pull never migrate
./scripts/vps-compose.sh run --rm --no-deps --pull never bot \
  check-config --config /etc/bot/config.yaml --connect
./scripts/vps-compose.sh up --no-build -d
```

僅使用本專案的 wrapper 管理容器；不需重啟 Docker 或 cloudflared。
其他備份還原與錯誤處理見 [operations.md](operations.md)。

Discord 安裝流程依據 [Discord 官方入門文件](https://github.com/discord/discord-api-docs/blob/main/developers/quick-start/getting-started.mdx)。
Ollama 參數依據 [OpenAI 相容 API 文件](https://docs.ollama.com/api/openai-compatibility)。
