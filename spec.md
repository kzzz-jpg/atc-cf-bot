# AtCoder／Codeforces Discord 每日題目 Bot 規格

| 項目 | 內容 |
| --- | --- |
| 文件版本 | v0.2 |
| 建立日期 | 2026-10-08 |
| 狀態 | 審核草案，尚未批准實作 |
| 專案 | `atc-cf-bot` |
| 文件語言 | 繁體中文 |
| 已確認技術棧 | Go（Golang）與 PostgreSQL；SDK、部署方式仍為架構提案。 |
| 架構審查 | 三輪對抗式規格審查；發現、修正及剩餘限制見第 19 節。 |

本文件定義第一版的功能、操作流程、設定、資料保存、失敗處理與驗收標準。原始需求及使用者指定的 Go／PostgreSQL 技術棧列為已確認要求；SDK 選擇、具體預設值及延伸行為均為提案。本次更新補完實作架構並記錄三輪對抗式審查，不代表開始實作或同意選配功能。

## 1. 目的與範圍

在指定 Discord 伺服器的 Forum Channel（討論區），每天自動建立一篇討論貼文，發布兩道從 AtCoder／Codeforces 隨機選出的題目。Bot 取得完整題目原文，透過 LLM 翻譯並整理為繁體中文，讓成員直接在討論串閱讀題目、取得提示與解答，再前往原站提交答案。

### 1.1 原始必要需求

| 編號 | 需求 |
| --- | --- |
| R01 | 每天自動發布兩道 AtCoder／Codeforces 隨機題目。 |
| R02 | 在 Discord 討論區建立每日題目的討論貼文。 |
| R03 | 每道題目提供前往原站的連結按鈕。 |
| R04 | 每道題目提供由 LLM 產生提示的按鈕。 |
| R05 | 每道題目提供由 LLM 產生解答的按鈕。 |
| R06 | 發布完整題目敘述，由 LLM 整理並翻譯。 |
| R07 | 第一版支援 OpenAI completion 相容格式；具體端點待確認。 |
| R08 | 提供手動「再傳一題」的指令。 |
| R09 | 可以設定 API key、LLM 模型、題目難度及運作所需設定。 |
| R10 | 額外推薦功能逐項審核，未批准前不納入實作。 |

### 1.2 第一版基本範圍提案

- 每天一篇貼文，兩道題目放在同一討論串。
- 發題時間、時區、來源組合、每日題數及兩平台難度可設定。
- 保存發題歷史，避免近期重複抽題。
- 提示與解答按需產生，預設僅點擊者可見。
- 支援手動抽題、查看今日貼文、手動執行每日發題及管理設定。
- 保存發布進度與生成快取，支援重啟恢復。
- 提供本機與 Docker Compose 啟動方式。

### 1.3 第一版不包含的功能

- 自動提交答案、登入競賽平台或自動評測生成程式碼。
- 執行使用者或 LLM 提供的任意程式碼。
- 一般聊天機器人、自由對話及讀取整個頻道作為 LLM 上下文。
- 網頁管理介面、完整排行榜或付費／配額計費系統。
- 帳號綁定、AC 同步、官方題解整合及其他尚未批准的選配功能。

## 2. 角色、權限與部署邊界

### 2.1 使用者角色

| 角色 | 可執行操作 |
| --- | --- |
| 一般成員 | 閱讀題目、開啟原站、要求提示或解答、查看今日貼文、手動抽題。 |
| 管理員 | 設定討論區、排程、抽題條件及一般設定；手動執行每日發題。 |
| 部署維護者 | 設定 Bot Token、LLM API key、base URL、模型與部署環境。 |

管理員預設定義為具有 Discord「管理伺服器」權限的人。一般成員的手動抽題權限可限縮到指定角色；未設定角色限制時，允許能使用指令並存取目標討論區的成員操作。

### 2.2 Bot 權限

- 目標討論區與討論串的檢視頻道、發送訊息、在討論串發送訊息及讀取歷史訊息權限。
- 嵌入連結及附加檔案權限，用於題目資訊、必要圖片及較長的私人回覆。
- 建立 Forum 貼文所需權限依 Discord 的 Forum API 規則檢查。
- 若實作需要重新開啟已封存討論串，另檢查相關權限；無法開啟時回覆明確原因。
- 不要求 Discord Administrator 權限。

啟動、設定目標討論區及正式發布前均檢查必要權限。使用 slash commands 與按鈕，不以讀取一般訊息內容作為第一版功能前提。

### 2.4 Discord 安裝、事件與 SDK 契約

- 使用 guild installation，邀請 scope 為 `bot`、`applications.commands`。指令不提供 DM／user-install 執行入口。
- Gateway 最小 intent 為 guild metadata 所需 intent，不啟用 MESSAGE_CONTENT 或成員全量同步。核對只讀 Bot 自己發送的內容；這類內容屬 Discord message content 的例外範圍。
- 管理指令設定 `default_member_permissions`，handler 仍重新驗證實際權限；指令可見性不能取代伺服器端驗證。
- 開發環境可指定測試 guild 註冊指令，正式環境使用固定 application 的全域指令；比對定義雜湊後才更新，不每次重連 bulk overwrite。
- 全域 router 解析 `v1:hint:<publication_uuid>`、`v1:solution:<publication_uuid>`；長度不超過 100 字元，不把題目敘述或模型參數放進 custom ID。
- 按鈕需同時驗證 guild、thread/channel、Bot 作者與已 confirmed 的按鈕 message ID，publication 至少已完成正文。UUID 不是授權憑證。未知版本回覆無法使用，不猜測路由。
- 來源與目標不同頻道的 `/problem random`，需重新確認點擊者可存取目標 Forum；缺少有效權限證據時拒絕。公開工作執行前重新查驗發起者的管理／角色與目標權限，撤銷權限後不繼續新增內容。
- 使用現有 message content＋embed＋action row 格式，不依賴 Components V2。每題三個按鈕放在一個 action row，URL 按鈕不帶 custom ID。
- 所有發送、編輯及 follow-up 使用 `allowed_mentions.parse=[]`，關閉 replied user mention。

Discord adapter 明確傳入 request context。公開 create 使用 `WithRestRetries(0)`，並關閉 SDK 內部自動 429 重試，由 adapter 依 `Retry-After` 與 deadline 決定下一次動作；保留 SDK 的 rate-limit bucket 排程，但不能讓 5xx／傳輸錯誤在紀錄外偷偷重送。若固定版本不能取消 rate-limit 等待，adapter 要補 context-aware transport／bucket 控制並以測試驗證。

固定 SDK 版本若缺少 `nonce/enforce_nonce`、Forum 起始訊息 ID 或其他必要欄位，使用同一 adapter 內的明確 JSON request／response 型別補足，不散落 raw API 呼叫。選定版本後必須做契約測試。

### 2.3 部署邊界

第一版為單一長時間執行的 Go Bot 程序，使用 PostgreSQL 保存設定、題庫、快取及任務進度。資料庫是必要依賴。設定與發題歷史以伺服器為單位隔離，部署端共用一組 LLM 連線設定。第一版不支援每個伺服器自行上傳不同 API key。

程序內可並行處理不同伺服器與私人生成，但每個伺服器的公開發布串行執行。第一版僅支援單一 Bot 副本，啟動時取得 PostgreSQL session advisory lock，防止意外啟動第二個副本；任務仍以持久化狀態及短交易處理，不能只依靠記憶體 mutex 保證恢復。

## 3. 每日貼文與使用體驗

### 3.1 貼文結構

目標頻道必須是既有 Forum Channel；Bot 不自動新建伺服器頻道。

```text
Forum Channel：每日練習
└─ 每日題目｜2026-10-08
   ├─ 首則訊息：日期、題目清單、發布狀態
   ├─ 題目一：資訊、完整中文敘述、範例與說明
   ├─ 題目一按鈕：前往題目／給我提示／給我解答
   ├─ 題目二：資訊、完整中文敘述、範例與說明
   ├─ 題目二按鈕：前往題目／給我提示／給我解答
   └─ 成員自由討論；手動加題也發在此處
```

標題預設為 `每日題目｜YYYY-MM-DD`，日期以伺服器設定的時區判定。首則訊息顯示原定每日題數、題目清單及目前狀態；完成後可加入各題訊息連結。

若討論區要求標籤，管理員需設定可使用的既有標籤 ID。Bot 不自行新增或修改 Forum 標籤。演算法題型標籤預設不顯示，避免提前揭露解法。

### 3.2 每道題目的內容

1. 原站名稱、題目編號、原題名稱及可選的中文名稱。
2. 原站題目連結、難度及難度資料來源。
3. 原題提供的時間限制與記憶體限制；缺少時不推測。
4. 完整繁體中文題目敘述。
5. 所有限制條件、輸入格式及輸出格式。
6. 所有範例輸入、輸出及原題範例說明。
7. 必要公式、圖片及其他影響理解的內容。
8. AI 翻譯標示及三個操作按鈕。

無難度資料時依抽題規則排除，不能把題目字母或配分當成難度。

### 3.3 訊息長度與格式

- 完整題目超過單則訊息長度時，依章節拆成多則訊息。
- 一般訊息內容不超過 Discord 的 2,000 字元限制；嵌入訊息及附件亦遵守其限制。
- 程式碼區塊、公式及範例區塊保持完整；過長區塊需要可還原的分段方式。
- 題目按鈕放在該題結尾訊息，不能只在每日總覽提供無法區分題目的按鈕。
- 題目全文以可直接閱讀的頻道訊息為主，不以附件或摘要取代完整題目。
- 禁止題目或 LLM 輸出觸發 `@everyone`、`@here` 或任意成員／角色提及。

### 3.4 Go renderer 與不可變 payload

- 不用 `len(string)` 判斷 Discord 字數；它計算 UTF-8 bytes。採 UTF-16 code units 保守計數，文字正文目標 1,800 units，完整 payload 的標籤、fence 及標點均納入 2,000 units 上限。
- 沿段落／行／安全字元邊界拆分，不切斷 UTF-8 編碼、代理對、組合字元、公式或受保護 token；拆分後檢查還原內容一致。
- 範例保持解析後的原始字串，僅統一來源 HTML 的換行表示，不 trim 行尾空白或刪除最後換行。訊息分段增加的 fence 與段落標題不屬於原始範例。
- 超長範例按完整行拆成多個連續 code block；單行過長則用明確續接段落，可還原原始字串，不能任意增加樣例內容。
- 每題公開正文與必要圖片最多 64 則訊息；超出時此題不支援，改抽其他題，不能只發布前 64 則。這是可配置的部署界限提案。
- 預先產生該題完整 payload 並驗證全部長度／附件限制，通過後才開始發第一段；最後一段附按鈕。
- 圖片從已保存 bytes 上傳，使用 `attachment://` 引用；恢復不依賴會變動或過期的 Discord CDN URL。
- Edit payload 明確指定保留的 attachment ID，避免更新總覽時意外移除附件。

## 4. 題庫、難度與抽題

### 4.1 資料來源

| 平台 | 題庫／難度 | 題目全文 |
| --- | --- | --- |
| Codeforces | 官方 `problemset.problems` API；比賽狀態由官方比賽資料判定。 | 官方公開題目頁。 |
| AtCoder | 非官方 AtCoder Problems 題目及比賽清單、`problem-models.json` 難度資料。 | AtCoder 官方公開題目頁。 |

CF 的 API 題目物件沒有完整敘述，因此全文解析是獨立流程。AtCoder Problems 的難度屬估計值，介面及文件須明確標示。

題庫每天更新一次並保存完整更新批次。更新期間不能讓候選查詢讀到一半的新題庫；只有整批資料通過驗證後才切換使用版本。更新失敗可使用最近成功的快取，但必須仍能驗證候選比賽已結束。CF 以已取得的 `FINISHED` 狀態判定；AtCoder 以已保存的開始時間加比賽長度判定。時間缺漏、未結束或來源解析不可信時排除。AtCoder Problems 存取間隔大於一秒；其他來源遵守官方限制及 `Retry-After` 回應。

### 4.2 難度語意

- CF 使用題目的 `rating` 數值，區間上下限均包含在內。
- AtCoder 第一版提案使用 `problem-models.json` 的 `difficulty` 原始估計值作為篩選欄位，介面標示其來源；若改採 AtCoder Problems 網頁顯示值，需確認換算規則並同步修訂文件及測試。
- AtCoder 與 CF 使用兩組獨立區間，不直接換算或共用一個 rating 範圍。
- 沒有難度估計的題目預設排除。

### 4.3 題目資格

- 僅選已結束且公開可存取的比賽題目。
- 第一版只處理一般程式題；排除互動題、輸出答案型題目、CF Gym 及需登入才能取得全文的題目。
- 不能只靠 CF `PROGRAMMING` 類型、題目 tags 或 AtCoder 題目編號判定題型。各平台解析器需檢查互動協定、特殊輸出指示及頁面標記；無法可靠分類時標為 `unsupported`，附理由並排除。
- AtCoder 第一版候選範圍提議為 ABC、ARC、AGC 的一般程式題。
- 題目必要內容必須可完整取得及轉換。無法正確解析的題目不可發布為完整題目。
- 每個候選題目使用穩定平台識別碼；同一 AtCoder task 出現在不同清單時仍視為同一題。

### 4.4 來源模式

| 模式 | 行為 |
| --- | --- |
| `balanced` | 每日題數為偶數時兩平台各半；奇數時以當地日期自 Unix epoch 起的日序奇偶決定額外來源，偶數給 AtCoder、奇數給 CF。每日兩題時固定各一題。 |
| `atcoder` | 全部選自 AtCoder。 |
| `codeforces` | 全部選自 CF。 |
| `weighted` | 每個名額依正數來源權重選平台，再從該平台合格題庫抽題。 |

預設為 `balanced`。不同平台的候選題量不影響 `balanced` 的來源配額。

### 4.5 抽題與去重

1. 取得本次設定快照，確認來源配額與難度條件。
2. 篩選合格題目、比賽狀態及完整性。
3. 排除同一伺服器近期已成功發布、當日已選定及其他進行中任務已保留的題目。
4. 從合格集合隨機抽取，不依列表順序偏好特定題目。
5. 抓取、解析及翻譯；單題失敗可在同條件下更換候選。
6. 該題第一則正文成功發布後即記錄 `first_published_at`，避免部分發布造成再次抽到。

去重預設 90 天，每日題與手動加題共用紀錄。每日同一份貼文內不允許重複；即使去重天數設為零，當日及進行中任務仍維持去重。

90 天窗口以 PostgreSQL UTC 時間減去 90×24 小時計算，不以日期文字比較。候選保留及紀錄寫入在同一伺服器的短交易鎖內完成；未開始公開發布、已確定失敗的候選可釋放，已部分發布或結果不明的候選不得逾時自動釋放。

候選取自保存的完整題庫快照，以已篩選題目 ID 集合做均勻抽樣；RNG 可注入但正式環境不能固定測試 seed。保留候選的短交易重新檢查去重及 reservation；並發衝突不算題目解析失敗，重新取候選，最多 20 次保留衝突後回覆稍後再試。題目抓取／解析／翻譯的實際候選次數仍受每個名額 5 題上限約束。

每個題目名額預設最多嘗試 5 個候選題。來源不足時不自動放寬難度、縮短去重期限或跨平台補位；保留未完成名額並通知管理員。

## 5. 原文解析、翻譯與完整性

### 5.1 原文解析

- AtCoder 優先使用英文題目；沒有英文版本時使用日文。
- CF 使用英文題目頁。
- 題目解析器保留章節順序，區分敘述、限制、輸入、輸出、範例、範例說明及其他必要章節。
- HTML 轉為可保存的結構化原文，移除導覽、廣告及與題目無關的頁面內容。
- 原文、來源 URL、抓取時間、內容雜湊及解析器版本一併保存。
- 原文版本建立後不可覆寫。後續重新抓取或更新解析器產生新版本；既有貼文與按鈕固定引用發布時的版本。
- 公式保留原始表示，再轉為 Discord 可理解的文字或符號；不能由 LLM 擅自重建缺失公式。
- 必要圖片需公開可存取並能展示；無法呈現或依賴圖像才能理解且文字不足的題目，第一版改抽其他題。

題目頁、HTML 解析後的必要文字及公式需能放入完整提示／解答請求的部署上限，預設完整原文含樣例不超過 64 KiB UTF-8；無法處理的長題在發題前排除。這是資源界限，不把 bytes 當成通用 token 計數。

### 5.2 LLM 翻譯規則

翻譯任務只負責忠實翻譯及改善排版，不得摘要、刪除條件、增加假設、加入提示或解答。

- 保留所有變數、數值、上下界、運算子與例外條件。
- 範例輸入及輸出由程式直接取自原文，不由 LLM 生成或改寫。
- 範例說明、註記及必要背景均翻譯。
- 長題目依原文章節分批翻譯；每批提供必要術語及變數背景。
- 模型輸出遭截斷時視為失敗，不能把截斷結果標記為完整翻譯。
- 超出可處理上下文或無法完成全部章節時改抽其他題，不以摘要降級。

### 5.3 完整性檢查

發布前至少檢查：

- 原文章節與翻譯章節的對應完整。
- 所有範例數量及輸入／輸出內容一致。
- 必要限制、輸入及輸出章節存在；原題沒有某章節時依來源實際結構處理。
- 必要圖片、公式及來源資訊均可呈現。
- 結果非空、格式可分段且未因輸出上限截斷。

這些檢查可發現缺漏與格式錯誤，不能保證語意及解法完全正確。發布內容標示為 AI 翻譯，保留原站連結供核對。

### 5.4 翻譯 block 與受保護內容

每個原文 block 均有穩定 ID。程式把 HTML math、變數、限制中的數值及運算式辨識為 protected atoms，使用不可與原文碰撞的 placeholder 送給 LLM；回覆必須保留所有 placeholder 的出現次數，再由程式還原原始內容。

- 每次翻譯一個 block 或具有可恢復子 ID 的段落，不要求供應商支援 JSON Schema。
- Block ID 由程式對應，不讓模型決定 AST 結構、來源 URL、附件或範例內容。
- LLM 可翻譯 prose，但不可重寫完整數學 atom；未識別的關鍵公式或無法完整還原的頁面標為不支援。
- 翻譯批次輸入提案上限 16 KiB（含必要背景），完整問題的生成總期限仍為 180 秒，不能每個 block 重設總期限。
- 單 block 被截斷時可拆成更小片段，最多兩層；仍須符合原文／生成總期限，不盲目用相同上限重送三次。
- 結果經 block 數量、placeholder multiset、樣例與格式檢查後才快取。完整條件的語意仍需人工核對，不能把 placeholder 檢查當成數學證明。
- 題目內「忽略前述規則」等文字照題目資料處理；生成程序沒有工具呼叫或系統密鑰上下文。格式／完整性不符時拒絕結果。

### 5.5 抓取與來源資源界限

- 頁面、題庫與圖片使用獨立 `http.Client`，不共用 LLM Authorization header。每次 HTTP 讀取均有 context、回應大小上限及 `Body.Close`。
- 題目與圖片 URL 僅接受 HTTPS，驗證 host、port、DNS 目的位址及每次 redirect；拒絕 localhost、私有／link-local／metadata 位址及 URL userinfo。只從已批准官方頁面及驗證過的官方資產 host 抓取。
- HTTP redirect 最多 3 次；LLM 端點不自動跟隨 redirect。部署者明確設定的本機相容 LLM 可使用 HTTP，例外不套用到題目抓取。
- 題庫 JSON 解壓後上限 64 MiB、題目 HTML 5 MiB、每張圖片 2 MiB、每題總圖片 8 MiB、LLM JSON 回應 4 MiB。超過上限是明確 unsupported/error，不讀進無限記憶體。
- 圖片驗證 MIME、實際解碼及像素上限（提案 16 megapixels）；不在訊息中發布活動 HTML／SVG，需可靠轉換或排除。
- 題庫 JSON 使用完整解碼、欄位驗證及尾端檢查；不可只因 HTTP 200 就將反爬頁面當題庫或原文。
- 來源 403、挑戰頁或全站 outage 分為來源暫停，不把所有候選永久標記不支援。個別題目的不支援理由依 parser 版本保存，預設 24 小時後可重新檢查。
- 不實作登入或繞過來源限制；來源無法完整讀取時按既有失敗規則處理。

## 6. 題目按鈕與生成回覆

### 6.1 前往題目

使用 Discord URL 按鈕直接開啟官方題目頁，不呼叫 LLM。

### 6.2 給我提示

- 使用完整原文作為依據，提供切入方向、關鍵觀察及適合嘗試的思考方式。
- 不只依賴中文翻譯，也不只依賴題名或平台 tags。
- 第一版提供單一提示，不預設包含完整演算法、完整證明或程式碼。
- 預設以繁體中文、ephemeral 訊息回覆點擊者。
- 分階段提示屬選配，未批准前不實作。

### 6.3 給我解答

解答包含：

1. 題意及解法所需的關鍵觀察。
2. 演算法與步驟。
3. 正確性說明。
4. 時間與空間複雜度，並對照原題限制。
5. 設定語言的參考程式碼，預設 C++17。
6. 必要邊界條件及範例說明。

解答預設以 ephemeral 訊息回覆，標示「AI 產生，未經原站評測」。第一版不抓取官方題解，不宣稱生成程式碼已 AC，也不自動執行程式碼。

### 6.4 互動與私人回覆

- 收到按鈕互動後，在 Discord 的 3 秒期限內 defer，顯示正在處理，再非同步生成。
- 生成操作預設總時間上限為 180 秒，包含排隊及重試；需在 Discord 15 分鐘互動 token 有效期內結束。
- 長回覆分為多則私人 follow-up；過長的提示、解答或程式碼可提供私人附件。
- 按鈕可在重啟後使用：以穩定 `custom_id` 對應已保存的題目發布紀錄，啟動時恢復處理器。
- 點擊者必須能存取該伺服器及貼文；不能以偽造識別碼取得其他伺服器的內容。
- 來源紀錄被刪除或無法恢復時，明確回覆無法提供，不生成無關題目內容。

### 6.5 快取、並發與冷卻

翻譯在發題時生成；提示與解答只在需要時生成。

快取鍵為規範化 request fingerprint 的 SHA-256，至少包括原文內容／AST 雜湊、parser／AST schema 版本、生成類型、供應商／完整 base URL 識別、模型、prompt 版本及模板雜湊、輸出語言、程式語言、token limit 參數名稱及其他會影響結果的生成參數。Endpoint 去除尾端 `/`，不得意外去除 API path。JSON key 排序、NULL 與未傳參數的區別需固定。快取不包含 API key。

- 相同快取鍵的並發請求合併為一次生成，各使用者仍收到自己的私人回覆。
- 提示及解答內容可以共用；使用者身份與互動 token 不進入共用快取。
- 每人生成請求冷卻預設 30 秒；限流適用於快取命中，以防止大量互動。
- 部署層 LLM 同時執行上限預設 2，排隊上限預設 20；超額時提示稍後再試。
- 冷卻與排隊拒絕不消耗 LLM 請求。
- API key 更換不直接失效內容快取；模型、內容或生成設定變更使用新的快取鍵。

### 6.6 生成協調與生命週期

- `generation_entries` 以唯一 cache key、狀態 `pending/running/ready/failed` 及 owner 協調。程序內用受 mutex 保護的進行中任務表合併訂閱者，不能讓第一個點擊者的 context 決定所有人的結果。
- 共用生成 context 屬於 application root，具有自己的 180 秒總期限；每位使用者另有從 defer 起算的等待期限。晚加入的使用者不延長生成期限。
- 最後一位私人訂閱者離開且沒有公開翻譯任務引用時，可以取消生成；有其他訂閱者時繼續。供應商可能已計費，不能承諾取消即可免除用量。
- 只有 `ready` 且完整性驗證成功才回傳成功快取；儲存失敗不得對外宣稱已完成快取。
- 全域待生成數量 20 指不同 cache key，不是等待者數量。等待者上限提案為全域 200、每伺服器 50；超額者不新增訂閱。
- 以兩條有界待生成佇列區分私人互動與公開翻譯。兩者均有工作時，最多連續開始三個私人工作後開始一個公開工作，避免每日翻譯長期飢餓；共用兩個 LLM 執行名額。
- 進行中的生成每 15 秒續租，租約 90 秒；更新結果檢查 owner。新程序取得 singleton lock 後，只將前任失去 owner 的生成標記失敗，等新請求再生成。
- 同一正常執行生命週期可合併請求，但外部 LLM 不提供本專案可依賴的持久化冪等性；重試或程序中止後可能重複計費，不能宣稱跨故障只呼叫一次。

私人 interaction token 僅保存在有期限的記憶體訂閱者中，不放入 PostgreSQL。重啟後不恢復私人回覆投遞；點擊者可以重新點按鈕取得已保存結果。公開發題 job 可恢復，公開指令的私人進度回覆則只在 token 仍有效的當次程序盡力投遞。

## 7. Slash commands

| 指令 | 參數 | 行為 | 權限 |
| --- | --- | --- | --- |
| `/problem random` | 可選 `source`、`min_difficulty`、`max_difficulty` | 抽取一題，發布到當日討論串。 | 一般成員／指定角色 |
| `/daily show` | 無 | 私人回覆今日貼文連結及發布狀態；尚無貼文時說明。 | 一般成員 |
| `/daily publish` | 無 | 執行或恢復當日每日發題；已完成時回傳既有貼文。 | 管理員 |
| `/config show` | 無 | 私人顯示一般設定及密鑰是否已設定，不回傳密鑰。 | 管理員 |
| `/config set` | `key`、`value` | 驗證並更新允許修改的一般設定。 | 管理員 |
| `/help` | 無 | 私人顯示指令及按鈕使用說明。 | 一般成員 |

### 7.1 手動加題規則

- 預設使用伺服器來源及難度設定。
- 未指定 `source` 時，`balanced` 的手動加題依當日已接受的手動名額交替來源，奇數名額為 AtCoder、偶數為 CF；`weighted` 在任務建立時抽一次並保存結果；單平台模式直接使用該平台。來源不足不自動改源。
- 指定難度時必須指定單一平台，避免上下限語意不明。
- 單次難度覆寫仍需落在管理員設定的可用範圍內；不修改持久化設定。
- 當日沒有貼文時建立每日總覽貼文，附加手動題；每日兩題名額仍由排程或 `/daily publish` 補齊。
- 手動題不扣每日配額，也不讓當日排程判定為已完成。
- 由當日主貼文串接，標示「加題 #N」及來源。
- 預設每位成員手動加題冷卻 60 秒，同一伺服器同時僅執行一個公開發題任務。
- 目標討論串已封存時嘗試重新開啟；遭鎖定或無權限時回覆失敗，不另建重複貼文。

### 7.2 設定更新規則

`/config set` 使用允許的 key 清單與型別驗證，不提供任意檔案路徑或任意資料庫寫入。清單型與配對型設定需使用明確格式，並在回覆中顯示解析結果。

執行中的任務使用開始時的設定快照。設定以版本號做樂觀並發控制；管理員同時修改同一版本時，一次成功，其餘收到衝突並重新讀取，不以整份 JSON 覆蓋他人的更新。

每日紀錄首次建立時保存當日的題數、來源、難度、去重、時區、討論區與標籤；即使貼文由手動加題先建立，也不在當日改寫這些欄位。後續當日手動題沿用相同抽題快照。解答語言、冷卻及角色權限設定在每次新互動時讀取最新版本，不改變題目原文版本。

一般設定儲存後在回覆中列出「立即生效」或「下一個每日週期生效」。今日已有紀錄時，公開發題設定下個週期生效；尚未建立紀錄時可作用於今日新任務。時區變更一律在舊時區當日結束後生效，避免一天中切換時區重複發題；依新時區算出的日期若已有每日紀錄，沿用該紀錄，不建立另一份。

排程時間更改後重新計算到期時間；若尚無今日紀錄且新的時間已過，下次排程檢查即視為到期。API key、base URL 及供應商連線參數僅由部署維護者修改。

### 7.3 指令接受、回覆與背壓

1. Router 在可用期限內給 ephemeral 初始回覆或 defer，不等待抓題或 LLM；若 defer 失敗，不能再開始此互動的公開副作用。
2. 驗證互動為伺服器內操作、來源頻道可用、目前角色與權限、參數及冷卻；被拒絕的互動不建立公開工作。
3. 公開指令在一個短交易中寫入 receipt、取得每日紀錄、配置名額及寫入 job；交易失敗則不接受任務。
4. 回覆「已接受」及今日貼文連結（若已存在），不讓私人 interaction token 綁住公開 job 的執行期限。
5. Worker 處理任務；若當次程序仍保存有效 token，可以更新完成資訊。重啟後用 `/daily show` 查看結果。

公開 job 的 dedup key 為 `daily:<daily_uuid>` 或 `manual:<guild_id>:<interaction_id>`。重複 Gateway 事件沿用既有 receipt／job，不配置第二個手動名額。Receipts 保存 24 小時，job dedup key 及發題名額長期保存，避免 receipt 清理後重複接受舊公開工作。

接受手動加題前，以同一短交易檢查待處理手動 job 上限：每伺服器 5、全域 100；不允許先寫入名額再宣稱佇列已滿。每日 job 不計入手動配額，各伺服器最多一個未結束的每日 job。私人冷卻以 `(guild_id, user_id, action_group)` 計算，通過其他驗證且接受請求後才消耗；重啟可重設記憶體冷卻，但持久化公開 job 去重仍有效。

全域手動配額在獨立 namespace 的 transaction advisory lock 內檢查，所有接受路徑採 admission lock → guild row → job／receipt 的固定順序，防止不同 guild 同時讀到第 99 個名額後都接受。這個鎖只覆蓋短交易，不占用長時間 singleton 的 key。已有 dedup key 的重複請求先回傳原任務，不再次消耗配額或冷卻。

## 8. 設定規格

### 8.1 密鑰

| 環境變數 | 必要性 | 說明 |
| --- | --- | --- |
| `DISCORD_BOT_TOKEN` | 必填 | Discord Bot Token。 |
| `LLM_API_KEY` | 必填 | OpenAI 或相容服務的 API key。 |
| `DATABASE_URL` | 必填 | PostgreSQL DSN，含連線憑證；不保存於伺服器設定。 |

本機可使用 `.env`，部署時可使用環境變數或部署平台的秘密管理。實作需提供不含真實密鑰的 `.env.example`，並忽略 `.env`。密鑰及含密碼的 DSN 不寫入資料表、指令回覆、例外訊息或一般日誌。

### 8.2 部署端設定

提議使用 `config.yaml`，環境變數可覆寫部署端設定。

| 設定 | 預設／要求 | 驗證 |
| --- | --- | --- |
| `llm.base_url` | `https://api.openai.com/v1` | 含 API 前綴，不含 `/chat/completions`；由部署端設定。 |
| `llm.model` | 必填，由使用者選擇 | 非空；不預選會產生費用的模型。 |
| `llm.translation_model` | 使用共用模型 | 可選覆寫。 |
| `llm.hint_model` | 使用共用模型 | 可選覆寫。 |
| `llm.solution_model` | 使用共用模型 | 可選覆寫。 |
| `llm.request_timeout_seconds` | 60 | 正整數。 |
| `llm.operation_timeout_seconds` | 180 | 大於單次逾時，小於互動 token 有效期。 |
| `llm.max_retries` | 2 | 首次請求之外最多兩次重試。 |
| `llm.output_token_limit` | 部署時指定，初始提案 8192 | 正整數，需符合模型限制。 |
| `llm.token_limit_parameter` | `max_completion_tokens` | 可改為相容服務支援的 `max_tokens`。 |
| `llm.temperature` | 預設不傳 | 僅模型支援時使用。 |
| `llm.max_concurrency` | 2 | 正整數。 |
| `llm.max_queue_size` | 20 | 非負整數。 |
| `llm.allow_missing_finish_reason` | false | 僅相容服務的明確部署例外，預設要求 `stop`。 |
| `database.url` | 由 `DATABASE_URL` 提供 | 必填，正式環境使用驗證伺服器憑證的 TLS 連線。 |
| `database.max_connections` | 10 | 正整數，不含 singleton lock 的獨立連線。 |
| `database.query_timeout_seconds` | 5 | 正整數，外部 I/O 不在 DB 交易內執行。 |
| `worker.public_concurrency` | 4 | 不同伺服器並行，每伺服器同時一個公開 job。 |
| `worker.lease_seconds` | 90 | 必須大於外部單次 60 秒逾時及心跳間隔之和。 |
| `worker.heartbeat_seconds` | 15 | 必須小於租約的一半。 |
| `scheduler.poll_seconds` | 30 | 不逐秒輪詢，目標觸發延遲不超過一個 poll 週期。 |

部署設定優先順序為內建預設 < `config.yaml` < 對應環境變數；明確空字串是錯誤，不回退到意外預設。YAML 未知欄位、重複 key、負數、NaN、權重總和不合法及互相矛盾的 timeout／lease 直接拒絕。用 Go 強型別解析後驗證，不能把任意 YAML 轉成未驗證的 DB JSON。

來源大小、公開分段、公開佇列、私人等待者上限使用第 3、5、6、7 節提案，作為部署端強型別 resource limits；管理員不能從 Discord 放寬部署上限。資料庫 credential 由環境提供，本機開發 TLS 例外需明確設定，不以正式部署預設關閉驗證。

### 8.3 伺服器設定

| 設定 | 建議預設 | 驗證／語意 |
| --- | --- | --- |
| `forum_channel_id` | 必填 | 屬於該伺服器的 Forum Channel，Bot 可存取。 |
| `forum_tag_ids` | 空清單 | 必須為該頻道既有標籤；頻道要求標籤時不得為空。 |
| `timezone` | `Asia/Taipei` | 有效 IANA 時區。 |
| `publish_time` | `09:00` | 當地時間，格式 `HH:mm`。 |
| `daily_count` | 2 | 提案範圍 1～10。 |
| `source_mode` | `balanced` | 第 4.4 節列出的模式。 |
| `atcoder_weight`／`codeforces_weight` | 1／1 | `weighted` 模式下為正數。 |
| `atcoder_min_difficulty`／`atcoder_max_difficulty` | 400／1000 | 整數，上限不小於下限。 |
| `cf_min_rating`／`cf_max_rating` | 800／1400 | 整數，上限不小於下限。 |
| `dedup_days` | 90 | 非負整數。 |
| `solution_language` | `cpp17` | 第一版語言候選提案：C++17、Python 3。 |
| `generation_cooldown_seconds` | 30 | 非負整數。 |
| `manual_post_cooldown_seconds` | 60 | 非負整數。 |
| `manual_post_role_ids` | 空清單 | 該伺服器既有角色，空清單代表未額外限制。 |
| `admin_log_channel_id` | 可選 | 同伺服器且 Bot 可發送訊息的頻道。 |

生成文字固定為繁體中文，題型標籤預設隱藏。第一版固定使用私人提示與解答，公開分享及其他語言不納入基本設定。

伺服器設定保存在 PostgreSQL；已有資料庫設定時不會被啟動預設值覆蓋。環境變數只覆寫部署端設定，不默默重置伺服器設定。

## 9. LLM API 相容層

### 9.1 提案採用 Chat Completions

本草案將「OpenAI completion 格式」解讀為 OpenAI 相容 Chat Completions。若使用者指的是舊式 `POST /v1/completions` 與 `prompt`，應在實作前修訂本節。

```http
POST {base_url}/chat/completions
Authorization: Bearer {api_key}
Content-Type: application/json
```

最低請求格式：

```json
{
  "model": "configured-model",
  "messages": [
    { "role": "system", "content": "任務規則" },
    { "role": "user", "content": "完整原文或待處理章節" }
  ],
  "stream": false
}
```

讀取 `choices[0].message.content` 作為文字回覆。供應商未回傳有效文字、回覆被截斷或格式不符時，視為失敗，不發布不完整內容。

有效結果需為 JSON、存在第一個 choice、文字非空且無 refusal／tool calls，並且 `finish_reason=stop`。`length`、`content_filter`、`tool_calls`、未知值及缺少 finish reason 預設不接受為完整結果。相容服務若省略 finish reason，部署端可明確開啟 `llm.allow_missing_finish_reason`（預設 false），但仍需完整性檢查，並記錄無法證明未截斷的相容限制。

### 9.2 相容與限制

- 不依賴 JSON Schema、工具呼叫、Responses API 或串流。
- 可設定輸出 token 上限參數名稱，避免假設所有模型支援相同選用參數。
- 三種任務分別使用版本化 prompt；原題當作資料，不當成系統指令。
- 不將 Discord 密鑰、LLM 密鑰、互動 token、一般聊天紀錄或不必要的成員資料送給 LLM。
- 若供應商回傳 `usage`，可保存用量作為除錯資料；未回傳時標記未知，不自行宣稱精確費用。
- 第一版透過單一 LLM adapter 隔離 HTTP 連線與供應商細節，為未來其他格式保留擴充點。

### 9.3 請求、重試與供應商錯誤

- 僅發送該部署配置支援的參數；不傳預設 `temperature`、`response_format`、`tools` 或不存在的模型能力。
- 每個 HTTP attempt 上限 60 秒，所有排隊、退避、分段及重試共用生成 180 秒總 context；剩餘時間不足不開始下一個 attempt。
- 網路／429／可恢復 5xx 採含 jitter 的退避，最多額外兩次；退避用可取消 timer，不用不可中斷 `time.Sleep`。
- 401／403 中止該 endpoint 的生成，400 中止對應模型／請求設定；錯誤配置標記不可用，避免每個候選再花一次請求。重啟或明確配置版本更新後重新驗證，不暗中改模型、token 參數或 endpoint。
- 錯誤回覆只記錄安全錯誤碼、HTTP status 及可用 request ID；供應商 body 不直接出現在 Discord 或日誌。
- 非串流回覆讀完並驗證後才產生成功結果；不把零 choices、HTML、錯誤 JSON、無文字或部分 body 視為解答。
- 對未知模型不猜測 tokenizer。輸入以 bytes 上限及模型設定界限預檢；供應商明確回報 context too long 時，翻譯可按規則縮小 block，提示／解答需完整原文，不截斷原文或摘要後繼續。

## 10. 排程、發布狀態與重啟恢復

### 10.1 日期與排程

- 排程以伺服器設定時區的當地日期與時間判定，資料庫時間戳使用 UTC。
- 每個伺服器、每個當地日期只建立一份每日發布紀錄與主貼文。
- 夏令時間導致當地時間不存在時，在該日下一個有效時間執行；時間重複時僅執行一次。
- 重啟後若當日已過發題時間且尚未完成，補發當天未完成名額。
- 不連續補發過去多日；過去未完成狀態保留供查詢與除錯。
- 修改時區或目標討論區不搬移已建立貼文；今日已有貼文時沿用原紀錄，新設定用於之後日期。

### 10.2 每日發布狀態

| 狀態 | 定義 |
| --- | --- |
| `pending` | 已建立紀錄，尚未開始每日題目準備；可已有手動加題貼文。 |
| `preparing` | 正在選題、解析或翻譯。 |
| `publishing` | 正在發送題目訊息。 |
| `partial` | 部分題目或訊息已發布，仍有未完成工作。 |
| `completed` | 原定每日名額全部完成。 |
| `failed` | 本次執行失敗，需要排程重試或管理員手動恢復。 |
| `needs_reconciliation` | 外部發布結果不明，停止新公開副作用，等待核對。 |
| `blocked` | 貼文刪除、鎖定或必要權限缺失，停止自動發布。 |
| `expired` | 當日週期結束仍有未完成名額，不再為此日期新增訊息。 |

每日紀錄保存原定題數、設定快照及各題目名額，避免設定變更使既有貼文反覆增減題數。手動加題獨立計數，不改變每日完成狀態。

### 10.3 發布與重複避免

- 使用資料庫唯一約束 `(guild_id, local_date)` 避免重複每日紀錄。
- 發布前保存穩定作業識別碼、題目與分段內容；逐段記錄 Discord message ID。
- Bot 重連、重複排程或 `/daily publish` 併發時合併至同一作業。
- 發送後斷線或程序中止、無法確定是否成功時，先核對 Discord 貼文／訊息，再決定是否重送。
- 貼文使用可辨識的日期及作業標記，訊息使用題目及段落標記，供恢復時比對。
- PostgreSQL 與 Discord 之間沒有跨系統交易；核對失敗時停止該部分並通知管理員，不盲目重送以宣稱絕對不重複。
- 一題已選定或已部分發布時優先恢復原題；不能換題後留下兩份混合題目。

啟動時恢復生成按鈕處理器與未完成作業，不依賴記憶體保存今日狀態。

### 10.4 Singleton、交易與 job 租約

Singleton lock 用獨立 `pgx.Conn`，以固定專案 namespace、database name 與 schema 決定 advisory key；run、寫入 reconcile 及 migrate 使用同一 key，避免各命令鎖不同資源。不能透過會切換 session 的一般 pool 查詢取得後就釋放連線，也不能透過 transaction-pooling proxy 持有 session lock。鎖連線需直連 PostgreSQL primary 或使用保留 session 的連線模式。

取得鎖失敗時第二個 Bot 程序直接退出，不啟動 Gateway／scheduler。每 5 秒確認鎖連線仍可用；確認失敗立即取消 application context、停止新副作用並關閉 Gateway，交給部署系統重啟。這是單副本防誤啟，不是多副本高可用保證。

Job 取出使用 PostgreSQL 短交易、`READ COMMITTED` 與列鎖。核心查詢概念：

```sql
SELECT j.id, j.guild_id
FROM posting_jobs AS j
JOIN guild_runtime AS g ON g.guild_id = j.guild_id
WHERE j.status IN ('queued', 'retry_wait')
  AND j.available_at <= clock_timestamp()
  AND j.deadline_at > clock_timestamp()
  AND g.block_reason IS NULL
  AND (g.resume_job_id IS NULL OR g.resume_job_id = j.id)
  AND (g.lease_until IS NULL OR g.lease_until <= clock_timestamp())
ORDER BY j.available_at, j.id
LIMIT 1
FOR UPDATE OF j, g SKIP LOCKED;
```

同一交易把 guild 的 `lease_epoch` 加一、將 owner／lease 寫入 guild 與 job，再提交；未取得兩者鎖時不執行工作。這個查詢只用於 queue claim，不作一般一致性讀取。

所有同時修改 guild 及 job 的路徑採相同鎖順序，並在 deadlock／serialization conflict 時只重試沒有外部副作用的短交易。`queued/retry_wait` 的 `deadline_at` 表示可開始工作的當日上限，claim 成功才設為 `min(DB now + 30 minutes, ends_at)`；重排 retry 時恢復當日可開始上限，避免舊 execution deadline 讓 job 永遠取不到。

- 租約 90 秒，每 15 秒用短交易心跳；不能把 DB 交易持有到抓題或發布結束。
- 作業狀態更新以 job ID、owner、lease epoch 及有效租約做條件更新，影響列數為零即停止舊 worker。
- 每次外部副作用前重新確認 lease。心跳失敗或失去 singleton 時取消該 worker context，不繼續發送。
- 租約到期的 `running` job 先進恢復判定；有 `sending/ambiguous` operation 時先核對，不能直接轉回 `queued` 重送。
- `pgx` context 取消不會自動 rollback；所有交易必須 `Commit` 或用獨立短清理 context `Rollback`，不能把失效 context 當成唯一清理手段。
- DB fencing 只能拒絕舊 worker 的資料庫寫入，無法收回已送到 Discord 的 HTTP 請求；外部結果仍依 10.5 核對。

### 10.5 Outbound operation 與外部效果核對

每次建立貼文、發送正文／圖片／按鈕或修改總覽，都先建立 operation，保存完整 payload、必要 asset ID、payload hash、renderer version 及順序。不得恢復時重新詢問 LLM 或重新渲染已送出的段落。

Operation 狀態：

```text
prepared → sending → confirmed
                 └→ ambiguous → confirmed / needs_reconciliation
prepared → failed（明確未接受的請求）
```

- 發送前先提交 `sending` 與時間，再做 HTTP；收到成功結果後提交 Discord ID。只有已 confirmed 的前一段才可送下一段。
- timeout、連線中斷、不明確 5xx 或成功但 DB 紀錄失敗，都視為 `ambiguous`。經 Discord 明確拒絕且沒有副作用的錯誤才可直接標失敗或按規則重試。
- 建立 Forum 貼文同時建立首則訊息，視為一個 operation，保存兩個外部 ID；不能分成兩次建立假裝原子。
- 正文使用 API 支援的穩定 `nonce` 與 `enforce_nonce`，nonce 不超過 25 字元。但 Discord 只在最近數分鐘檢查，不把它當成永久去重或 Forum 建立保證。
- 發送正文的核對條件為已知討論串、Bot 作者、可讀的日期／題號／段落標籤，以及凍結的內容與附件指紋。標籤用於閱讀順序，不在正文顯示內部 UUID。
- 貼文建立結果不明時，查詢目標 Forum 的 active 及 archived threads，再核對作者、建立時段、日期標題與凍結首則內容。不能只靠同名標題認領使用者貼文。
- 掃描最多 20 頁，每頁依 API 上限取資料；找到唯一吻合紀錄才認領。找到多份、掃描受限、缺權限或找不到均不等於未發送，不自動重建。
- 總覽 edit 因外部 ID 已知，可重新套用相同內容；遠端內容已等於凍結目標時視為已確認，仍等於保存的 `expected_before_hash` 才送 edit，其他修改報衝突，不刪除／改寫成員訊息。

不確定狀態保存為 `needs_reconciliation`，阻擋該伺服器的後續公開 job，避免兩份貼文或訊息交錯。私人生成仍可處理已完成的題目。

維護 CLI `reconcile` 預設只輸出核對報告。需要人工處理時，可指定已存在的 thread/message ID 讓 Bot 驗證並認領，或由維護者明確確認某 operation 未送達後才允許重送；每次決定保存時間、操作者標籤、理由及結果。此 CLI 是恢復機制，不新增未批准的 Discord 功能。

### 10.6 午夜、過期、重試與公平性

- Scheduler 每 30 秒讀取 DB 時間，推進各伺服器每日週期；跨多日停機只建立目前日期的紀錄。
- 同一伺服器的 job 按接受時間串行。單次 job deadline 為開始執行後 30 分鐘與當日 `ends_at` 的較早者；佇列中尚未開始的 job 到午夜即過期。
- 外部請求不得在 deadline 後開始；過期時取消 context，尚未送出的段落不再發布。已發出的請求仍需核對並保存結果。
- 舊日期已 confirmed 的段落保留；未完成者標為 `expired`。舊日只允許核對既有效果，不自動補發內容。
- 暫時性失敗可延遲 15 分鐘恢復，同日最多兩次自動恢復。每次重新開始更新 execution deadline，但不超過當日 ends_at；候選題嘗試數保存在名額，不因程序重啟清零。
- 認證／配置錯誤、無候選、被刪除／鎖定及不明確外部效果不消耗兩次自動恢復來反覆撞同一問題。明確修正後，當日 `/daily publish` 可恢復可處理的失敗；人工刪除貼文不自動重建。
- 午夜釋放未公開且結果確定的候選。部分公開的候選進去重歷史後釋放；ambiguous 候選在核對完成前持續保留，不能因日期更新消失。
- 未解決 ambiguous operation 阻擋後續公開工作並通知管理員，不讓新日排程繞過問題；解決後只發布當天，不補多日。
- 等待 `retry_wait` 期間不持有 guild 執行租約；進入等待前釋放 lease。下一個公開 job 開始前核對舊 job 已無未確認效果，再繼續，不能跳過有部分正文的舊題。
- 已開始某題正文但尚未完成的 job 以 `guild_runtime.resume_job_id` 保留發布順序，retry 等待時其他同 guild job 不可超越；該題完成、明確終止或午夜確認無不明確效果後才清除。租約用於執行互斥，resume pointer 用於正文不交錯，兩者不混用。
- 可先準備多道題，但發布題目一的所有段落及按鈕後才發布題目二，手動題不插入某題正文中間。

### 10.7 子狀態與每日完成判定

| 實體 | 狀態及主要轉移 |
| --- | --- |
| Job | `queued → running → succeeded`；暫時性失敗 `running → retry_wait → running`；明確不可恢復錯誤 `failed/blocked`，效果不明 `needs_reconciliation`，日期超過 `expired`。 |
| 題目名額 | `empty → reserved → preparing → ready → publishing → completed`；未公開候選失敗可回到 empty 並增加候選次數；公開後只恢復同一題。終止時為 `failed/expired`。 |
| 生成結果 | `pending → running → ready`；失敗 `failed`，新請求符合規則時重新 claim，但不覆寫已有 ready 結果。 |

每日 `completed` 只看原定 daily 名額及已確認的完整正文／按鈕，不依賴手動名額或當次 worker 是否退出。手動加題失敗不把已完成的每日兩題改成 failed；其錯誤保存在手動 publication／job。Forum 建立或原定 daily 名額受阻時才影響 daily 狀態。

伺服器的 `block_reason` 獨立保存，任何公開 operation 需要核對時可阻擋後續工作，而不改寫已完成的每日歷史。總覽可顯示「每日 2/2 完成；加題 #3 等待處理」，不能把兩種配額合併。

每日首次出現的永久缺候選保留可處理錯誤，但排程不可每 30 秒重新接受同一失敗 job。每個錯誤的 `retryable`、`available_at` 與恢復條件明確保存；當日手動 publish 的重新接受檢查目前阻擋原因並沿用同一名額與 job。

## 11. 失敗處理與通知

| 情境 | 行為 |
| --- | --- |
| 缺少 Bot Token 或 LLM 設定 | 清楚報告缺少欄位；不顯示密鑰，不啟動不完整服務。 |
| 目標頻道類型錯誤／權限不足 | 停止該伺服器發題，提示管理員修正。 |
| 題庫更新失敗 | 使用可驗證的既有快取；無可用資料則停止抽題。 |
| 來源沒有合格題目 | 保留未完成名額，說明來源、難度及去重條件，不自動放寬。 |
| 抓取失敗或題目解析不完整 | 同條件改抽候選；達候選上限後回報。 |
| LLM 401／403 | 不重試；回報連線或權限設定問題。 |
| LLM 400 或不支援參數 | 不盲目重試相同請求；回報配置錯誤。 |
| 429、逾時、連線錯誤、暫時性 5xx | 在任務總期限內有限次退避重試，遵守 `Retry-After`。 |
| 翻譯缺漏／截斷 | 有限次重試；仍失敗時改抽或標記未完成。 |
| 私人生成失敗 | 回覆可理解原因及稍後重試資訊，不張貼公開解答。 |
| Discord 發布部分成功 | 保留已發訊息，標記 `partial`，下次只恢復未完成部分。 |
| Discord 發布結果不明 | 先核對，核對失敗則通知管理員。 |
| 貼文遭人工刪除／鎖定 | 標記受阻，不自動重建已刪除的貼文。 |
| 管理通知頻道不可用 | 寫入已去除密鑰的部署日誌，不反覆發送通知。 |

每個伺服器單次公開 job 執行上限為 30 分鐘，重試、午夜與過期依 10.6。候選已完整解析後遇到 LLM 401／403 或參數錯誤，停止該生成配置的工作，不藉由換候選題反覆呼叫。當日管理員可在問題修正後用 `/daily publish` 恢復。

通知包含作業類型、日期、失敗階段及可處理原因。相同失敗以作業為單位去重，避免洗版。未設定管理通知頻道時只保存日誌及發布狀態。

## 12. PostgreSQL 資料模型

### 12.1 型別及不變量

- 內部 ID 使用 UUID，由應用程式產生，不依賴額外 PostgreSQL extension。
- Discord snowflake 使用十進位 `text`，Go 中使用 `string`；不轉成 JavaScript number 或有號 `int64`。新增時驗證只含數字及合理長度。
- 絕對時間使用 `timestamptz`；`local_date` 使用 `date`；當地發題時間使用 `time without time zone`，另保存 IANA 時區。
- 查詢、去重及租約以 DB 時間為準；HTTP／LLM deadline 使用 Go `context` 的持續時間。
- 可演進的設定、AST、payload 保存為 `jsonb`；狀態、查詢條件、外鍵及版本使用獨立欄位。不能只存一個大型 JSON 當全部資料模型。
- 狀態使用 `text` 與 CHECK 約束，避免每次新增狀態均需修改 PostgreSQL enum。
- 所有持久化內容包含 schema／renderer／parser／prompt 等必要版本，舊貼文不用新版本重新渲染。

### 12.2 資料表契約

| 資料表 | 核心欄位 | 約束／用途 |
| --- | --- | --- |
| `guild_configs` | `guild_id text PK`、`version bigint`、`settings jsonb`、`pending_schedule jsonb`、`pending_effective_at timestamptz`、`updated_at` | `version > 0`；設定寫入先驗證，更新使用預期版本。 |
| `guild_runtime` | `guild_id PK/FK`、`next_due_at`、`cycle_end_at`、`active_daily_id`、`block_reason NULL`、`resume_job_id NULL`、`lease_owner uuid`、`lease_until`、`lease_epoch bigint` | 伺服器排程與公開任務互斥；狀態不與設定 JSON 混在一起。 |
| `catalog_snapshots` | `id uuid PK`、`platform`、`status`、`fetched_at`、`source_hash`、`parser_version` | 每次完整題庫同步一版；未完成快照不提供給抽題。 |
| `catalog_heads` | `platform PK`、`snapshot_id FK`、`updated_at` | 原子切換目前 ready 快照，驗證 platform 與 snapshot 一致。 |
| `problems` | `id uuid PK`、`platform`、`external_id`、`canonical_url`、`created_at` | UNIQUE `(platform, external_id)`；AtCoder 使用 task ID，CF 使用 contest ID＋index。 |
| `catalog_entries` | `snapshot_id FK`、`problem_id FK`、`contest_id`、`difficulty integer NULL`、`contest_end_at`、`contest_finished`、`kind`、`metadata jsonb` | PK `(snapshot_id, problem_id)`；原始難度允許負值，缺值與零不同。 |
| `statement_versions` | `id uuid PK`、`problem_id FK`、`language`、`content_hash`、`parser_version`、`ast jsonb`、`raw_html text`、`fetched_at` | UNIQUE `(problem_id, content_hash, parser_version, language)`；建立後 immutable。 |
| `statement_assets` | `id uuid PK`、`sha256`、`mime_type`、`byte_size`、`body bytea`、`source_url` | UNIQUE `sha256`；保存必要圖片，避免遠端 URL 失效導致恢復改變內容。 |
| `statement_asset_links` | `statement_id FK`、`asset_id FK` | PK `(statement_id, asset_id)`；AST 的 asset ID 必須能由此查到。 |
| `source_rejections` | `problem_id FK`、`parser_version`、`reason_code`、`checked_at`、`recheck_after` | PK `(problem_id, parser_version)`；不把全站 outage 當永久題目失敗。 |
| `generation_entries` | `cache_key text PK`、`statement_id FK`、`kind`、`request_fingerprint jsonb`、`status`、`owner uuid`、`lease_until`、`result jsonb NULL`、`usage jsonb NULL`、`updated_at` | 只讀 `ready` 結果；生成失敗及進行中不作成功快取。 |
| `daily_publications` | `id uuid PK`、`guild_id FK`、`local_date`、`starts_at`、`ends_at`、`timezone`、`forum_channel_id text`、`config_snapshot jsonb`、`target_count`、`thread_id text NULL`、`status`、`created_at` | UNIQUE `(guild_id, local_date)`、UNIQUE `(id, guild_id)`；`ends_at > starts_at`、`target_count BETWEEN 1 AND 10`。 |
| `problem_publications` | `id uuid PK`、`daily_id`、`guild_id`、`kind`、`slot_no`、`problem_id NULL`、`statement_id NULL`、`generation_key NULL`、`source`、`catalog_snapshot_id FK`、`candidate_attempts`、`status`、`first_published_at NULL`、`completed_at NULL` | FK `(daily_id, guild_id)`；UNIQUE `(daily_id, kind, slot_no)`；每日與手動名額分開。 |
| `problem_reservations` | `guild_id FK`、`problem_id FK`、`publication_id FK`、`reserved_at` | PK `(guild_id, problem_id)`；同伺服器同一題僅有一個進行中保留。 |
| `posting_jobs` | `id uuid PK`、`guild_id FK`、`daily_id`、`publication_id NULL`、`requested_by text NULL`、`dedup_key text`、`kind`、`status`、`available_at`、`deadline_at`、`attempts`、`auto_retry_count`、`lease_owner NULL`、`lease_until NULL`、`lease_epoch bigint`、`last_error_code`、`retryable` | UNIQUE `dedup_key`；每日恢復更新同一 job，不因重試建立重複名額。 |
| `outbound_operations` | `id uuid PK`、`daily_id`、`publication_id NULL`、`sequence_no`、`kind`、`payload jsonb`、`payload_hash`、`renderer_version`、`expected_before_hash NULL`、`marker`、`status`、`discord_id text NULL`、`result jsonb NULL`、`send_started_at NULL`、`last_error_code` | UNIQUE `(daily_id, sequence_no)`、UNIQUE `marker`；result 保存 thread／起始 message 等複合結果。 |
| `reconciliation_decisions` | `id uuid PK`、`operation_id FK`、`actor_label`、`decision`、`reason`、`adopted_discord_id NULL`、`created_at` | 保存維護者認領／確認未送達的決定，不保存互動 token 或密鑰。 |
| `interaction_receipts` | `interaction_id text PK`、`guild_id`、`kind`、`posting_job_id NULL`、`accepted_at`、`expires_at` | 重複互動只能接受一次；不保存 Discord interaction token。 |
| `goose_db_version` | goose 管理的 migration 版本紀錄 | 明確 migration 命令執行，不自建另一套版本表或由 Bot 啟動任意修改 schema。 |

`statement_id` 與 `problem_id` 的一致性使用複合外鍵約束，對應 `statement_versions(id, problem_id)` 唯一鍵。相同 `daily_id`、`problem_id` 建立非 NULL 部分唯一索引，防止每日及手動題重複。涉及伺服器的子實體透過複合外鍵與父實體綁定；repository 查詢必須攜帶 `guild_id`，不能只相信 UUID 難以猜測。

具體關聯需包含 `posting_jobs(daily_id, guild_id)` → daily 的複合鍵、`posting_jobs/publications` 的同日關聯、`outbound_operations(publication_id, daily_id)` → publication 同日鍵，以及 `problem_reservations(publication_id, guild_id, problem_id)` → publication 對應唯一鍵。可空 FK 採允許父 operation 的一致用法，非空時必須完整。

狀態 CHECK 依第 6、10 節建立；`ready` 生成必須有非空 result，`confirmed` operation 必須有有效外部結果，`completed` publication 必須有完成時間。選定的原文版本與 generation entry 的 statement 相符後才能準備 outbound。公開發題版本引用的 ready 結果不可刪除或覆寫。

### 12.3 必要索引

- `catalog_entries(snapshot_id, difficulty, problem_id)`：難度候選。
- `problem_publications(guild_id, first_published_at DESC, problem_id)`，限已公開的列：去重歷史。
- `posting_jobs(available_at, id)`，限 `queued/retry_wait`：取出到期任務。
- `posting_jobs(lease_until)`，限 `running`：恢復租約失效任務。
- `outbound_operations(daily_id, sequence_no)`：按序發布及核對。
- `daily_publications(guild_id, local_date DESC)`：當日與歷史查詢。
- `interaction_receipts(expires_at)`：清理過期接收紀錄。

### 12.4 保存與刪除

- 發題歷史保留，不因去重窗口結束而刪除。
- 已引用原文、翻譯及 outbound payload 不可自動刪除；必要圖片保存於 PostgreSQL，Bot 不要求本機附件 volume。
- 未引用的失敗候選與快取可日後清理，第一版先保留並記錄容量；歷史快照清理不可破壞外鍵及恢復。
- 資料庫備份與還原包含原文、圖片、設定、歷史及進度；管理員刪除 Discord 貼文不級聯刪除發題紀錄。
- 同一題目的原文及共用模型生成結果可跨伺服器共用；角色、伺服器歷史及互動資訊不可共用。

## 13. Go 實作架構

### 13.1 技術選擇

| 部分 | 決定／提案 |
| --- | --- |
| 語言 | **Go，使用者已指定**；實作時固定受支援的 toolchain 版本。 |
| 資料庫 | **PostgreSQL，使用者已指定**；部署提案 PostgreSQL 18，固定映像版本／digest。 |
| Discord SDK | `github.com/bwmarrin/discordgo`；Gateway 接收 slash command 與按鈕事件，REST 發布。 |
| DB driver | `github.com/jackc/pgx/v5`、`pgxpool`；參數化 SQL，不以 ORM 隱藏交易與鎖。 |
| Migration | `github.com/pressly/goose/v3` 的 SQL migration；明確部署步驟。 |
| HTTP／LLM | Go `net/http`、`encoding/json`，以薄 adapter 實作 Chat Completions。 |
| HTML | `golang.org/x/net/html` 配合平台專用解析器，可使用 goquery 選取節點。 |
| 排程／並發 | `time`、`context`、有界 worker pool 與 PostgreSQL 任務表。 |
| 設定／日誌 | YAML 解析與強型別驗證、標準庫 `log/slog`。 |
| 部署 | Go 多階段建置容器＋PostgreSQL；資料只需 PostgreSQL 持久化。 |

SDK 與函式庫版本在實作時固定於 `go.mod`／`go.sum`，不以浮動 master 作為正式依賴。Go 版本與 PostgreSQL 小版本不在規格階段宣稱已驗證。

### 13.2 套件邊界

```text
cmd/atc-cf-bot/       CLI：run、migrate、check-config、reconcile
internal/app/        依賴注入、啟動、健康狀態、關閉
internal/config/     環境變數、YAML、驗證、設定版本
internal/domain/     題目 AST、狀態、抽題政策、純函式
internal/discord/    discordgo adapter、指令／按鈕 router、權限
internal/catalog/    來源同步、快照、候選篩選
internal/source/     AtCoder／CF HTTP 與 HTML 解析
internal/generation/ 翻譯／提示／解答、快取、任務排隊
internal/llm/        OpenAI 相容 HTTP adapter
internal/render/     Discord Markdown、UTF-16 長度、圖片與分段
internal/posting/    名額、保留、公開發布、核對與恢復
internal/scheduler/  到期掃描、日期與時區週期
internal/store/      repository 介面、PostgreSQL 交易實作
internal/worker/     任務領取、租約、背壓與取消
migrations/          SQL migrations
prompts/             版本化翻譯／提示／解答模板
testdata/            HTML、API JSON、AST 與故障樣本
```

這是未來程式碼結構契約，不代表已建立這些檔案。依賴方向為 adapter → application service → domain／repository 介面；domain 不引用 discordgo、pgx 或 HTTP 型別。交易操作以用途明確的 repository 方法提供，不用橫跨所有服務的巨大通用 repository。

### 13.3 核心型別及介面契約

```go
// 介面草圖；具體型別在實作時定義。
type ProblemSource interface {
    SyncCatalog(ctx context.Context) (CatalogSnapshot, error)
    FetchStatement(ctx context.Context, ref ProblemRef) (Statement, error)
}

type LLMClient interface {
    Complete(ctx context.Context, request CompletionRequest) (CompletionResult, error)
}

type DiscordPublisher interface {
    Execute(ctx context.Context, operation OutboundOperation) (DiscordResult, error)
    Reconcile(ctx context.Context, operation OutboundOperation) (ReconcileResult, error)
}

type PostingStore interface {
    AcceptDaily(ctx context.Context, request DailyRequest) (JobRef, error)
    AcceptManual(ctx context.Context, request ManualRequest) (JobRef, error)
    ClaimJob(ctx context.Context, owner string) (JobLease, error)
    ReserveCandidate(ctx context.Context, lease JobLease, candidate ProblemRef) error
    RecordOutboundResult(ctx context.Context, lease JobLease, result OutboundResult) error
}
```

所有 I/O 傳遞 `context.Context`，錯誤以型別／錯誤碼區分 `Unauthorized`、`Unsupported`、`NoCandidate`、`Transient`、`RateLimited`、`Ambiguous` 及 `Conflict`。使用者回覆由錯誤碼生成，不直接輸出 HTTP body 或底層連線錯誤。

題目 AST 至少有 `schema_version`、來源語言、順序固定的 `blocks`、原始範例及 asset 參考；每個 block 有穩定 ID、章節種類、文字及需保留的公式／數值 token。翻譯結果使用 block ID 對應，renderer 檢查一一對應後才接受。提示／解答的來源必須是已保存的完整原文版本。

純抽題、日期判定、長度及渲染不依賴外部服務。隨機數來源及 clock 可注入，以固定測試重現候選與日期。

### 13.4 啟動與命令生命週期

`run`：

1. 讀取並驗證設定，建立有期限的 logger／HTTP client；不輸出密鑰或 DSN。
2. 連接 PostgreSQL，檢查 schema 版本與程式相容；版本不足時退出並提示先 migrate。
3. 取得專用 singleton lock；建立最多 10 條連線的 pgxpool，設定 query timeout。
4. 設定 application root context、worker 與共享生成協調器；恢復前任任務與不明確 operation，恢復完成前不開始新公開工作。
5. 連接 Discord Gateway，確認 application／Bot identity，註冊 router 及必要指令差異。
6. 啟動題庫同步、排程 poll、worker、心跳及健康端點；未配置 Forum 的 guild 仍可使用 help／config，不建立發題 job。

`migrate` 是顯式命令，使用同一 singleton lock，拒絕與 run／寫入 reconcile 並行；只依賴 DB 配置，不要求呼叫 LLM 或 Discord。部署先備份，再執行 migration，再啟動新版本。失敗時保留工具狀態，不能假裝版本已升級或自動重建資料庫。Migration 版本檢查包含實際 schema 與工具成功紀錄。

`check-config` 預設只做本機型別／欄位檢查；`--connect` 可驗證 DB、Bot 權限及端點連線，但不建立貼文或生成付費內容。

`reconcile` 的唯讀報告可與 run 並行；任何狀態修改或允許重送的決定必須先停止 run，取得同一 singleton lock，經既有資料核對後才更新。正常 worker 不能與人工恢復同時操作相同貼文。

### 13.5 部署、健康狀態與關閉

- Compose 至少包含 `postgres` 與 `bot`，另提供顯式 migration 命令或一次性 migration service；Bot 等待 DB healthy 及 migration 成功，不在 run 內悄悄執行 migration。
- PostgreSQL 資料 volume 按選定 image 版本的實際資料目錄掛載；不直接沿用其他 PostgreSQL major 的路徑。先驗證容器重建及 restore，再宣稱可恢復。
- Bot 使用非 root 多階段容器，附 CA certificates 與 IANA tzdata（可用 Go `time/tzdata`），程式可唯讀執行，不依賴工作目錄保存狀態。
- `GET /livez` 表示事件迴圈／程序可回應；`GET /readyz` 表示 DB 可用、singleton 有效、Gateway ready 且初始化完成。題庫或 LLM 暫時不可用屬 degraded，回覆安全摘要，不因供應商 outage 反覆重啟。
- 健康端點只供部署內網，不回傳密鑰、使用者內容或高基數資料。日誌記錄安全 job／operation ID、錯誤碼、HTTP status、stage 及 duration；不記錄一般全文、Authorization 或 interaction token URL。
- 收到 SIGTERM／SIGINT 後先使 readiness 失敗並停止接受／領取新工作，取消工作與 SDK request context；最多等待 30 秒寫回完成／不明確狀態。
- 關閉 Gateway、worker、HTTP idle connections 及 pool；在 worker 停止後最後釋放 singleton session。清理使用獨立短期限 context，不使用已取消的 application context。
- DB 無法寫回時保留先前 `sending/running` 狀態，由下次啟動核對。關閉成功不代表已取消 Discord 端的請求。
- 備份還原後啟動 `--recovery-mode`：關閉新公開 job 接受及排程，只核對 restore 之前可能已送出但備份缺失的效果。先採用或人工處理孤立貼文／訊息，再由維護者明確解除恢復模式；不能把舊備份當成 Discord 也已回滾。

### 13.6 最小設定範例

以下是未來交付範例，不含真實密鑰，未設定模型時 check-config 必須失敗：

```dotenv
DISCORD_BOT_TOKEN=<由部署環境提供>
LLM_API_KEY=<由部署環境提供>
DATABASE_URL=<PostgreSQL DSN，勿提交至 Git>
```

```yaml
llm:
  base_url: https://api.openai.com/v1
  model: "" # 部署者填寫
  request_timeout_seconds: 60
  operation_timeout_seconds: 180
  max_retries: 2
  output_token_limit: 8192
  token_limit_parameter: max_completion_tokens
  allow_missing_finish_reason: false
  max_concurrency: 2
  max_queue_size: 20
database:
  max_connections: 10
  query_timeout_seconds: 5
worker:
  public_concurrency: 4
  lease_seconds: 90
  heartbeat_seconds: 15
scheduler:
  poll_seconds: 30
```

Go SQL 使用 positional parameters，表名／schema 不接受 Discord 輸入拼接。權重、難度等一般設定在 transaction 中寫入已驗證結構；配置中的 LLM base URL 只有部署維護者可改，避免使用者把密鑰請求導向任意主機。

## 14. 驗收條件

### 14.1 功能驗收

| 編號 | 驗收情境 | 預期結果 |
| --- | --- | --- |
| A01 | 到達設定時間 | 建立當日一篇 Forum 貼文，發布原定兩題。 |
| A02 | 預設 balanced 模式 | AtCoder、CF 各一題，兩者均符合各自難度範圍。 |
| A03 | 發布完整題目 | 所有必要章節、範例、公式與圖片保留，沒有摘要取代全文。 |
| A04 | 長題目 | 自動分段，訊息不超限，程式碼及範例可正確閱讀。 |
| A05 | 點擊原站按鈕 | 開啟該題官方頁面。 |
| A06 | 點擊提示／解答 | 及時 defer；只向點擊者提供對應題目的內容。 |
| A07 | 多人同時要求同一解答 | 合併一次生成，各人取得私人回覆。 |
| A08 | 執行手動抽題 | 同日貼文增加一題，不減少每日兩題名額。 |
| A09 | 手動加題先於排程 | 建立的貼文供排程沿用，每日兩題仍正常補齊。 |
| A10 | 查看及修改設定 | 管理員可操作；不合法值被拒絕，不改動既有設定。 |
| A11 | 一般成員執行管理指令 | 被拒絕，無設定或公開發布副作用。 |
| A12 | 更換相容 API 端點 | 透過部署設定使用相同基本請求格式取得文字回覆。 |

### 14.2 可靠性與資料驗收

| 編號 | 驗收情境 | 預期結果 |
| --- | --- | --- |
| A13 | 同日重複排程及手動 publish | 沿用同一每日紀錄及貼文，不重發已完成題目。 |
| A14 | 第二題發布失敗後重啟 | 保留第一題，恢復第二題或其剩餘段落。 |
| A15 | 發送成功但未記錄時中止 | 恢復時核對既有訊息；不明確時停止並通知。 |
| A16 | Bot 重啟後點擊舊按鈕 | 正確載入該題版本並回覆。 |
| A17 | 過了當日排程時間才啟動 | 補發當天未完成名額，不補發過去多日。 |
| A18 | 無符合條件的題目 | 不自行放寬條件，標示未完成並記錄原因。 |
| A19 | LLM 限流、逾時及認證錯誤 | 依錯誤類型處理，遵守次數及總時間上限。 |
| A20 | 範例輸出被 LLM 改寫 | 發布結果仍使用原始範例，不能採用模型改寫版本。 |
| A21 | 偽造跨伺服器的題目按鈕 ID | 權限驗證拒絕存取。 |
| A22 | 查看設定或錯誤日誌 | 不出現 API key、Bot Token 或互動 token。 |
| A23 | 更換模型、prompt 或原文版本 | 使用新的快取鍵，不回傳舊版本結果。 |
| A24 | Docker 容器重建 | 設定、歷史、進度及舊按鈕資料仍可恢復。 |

### 14.3 三輪審查新增的驗收

| 編號 | 反例／驗收情境 | 預期結果 |
| --- | --- | --- |
| A25 | 同時啟動兩個 run | 僅一個取得 singleton 並開 Gateway；另一個退出。 |
| A26 | Pool session 更換／transaction proxy | 不使用 pool session 作鎖；不合法鎖連線模式被拒絕。 |
| A27 | 兩個 worker 同時 claim 同 guild | 僅一個取得 guild 與 job；其他 guild 可並行。 |
| A28 | 租約過期後舊 worker 寫入／發送 | 條件更新失敗，停止新增副作用，已發送請求進入核對。 |
| A29 | PostgreSQL 交易中 context 取消 | 清理 rollback／連線釋放，pool 不被未結束交易耗盡。 |
| A30 | Forum 成功但回應丟失／同名成員貼文 | 唯一正確 Bot 貼文才認領，無證據則停住，不認領成員貼文。 |
| A31 | 核對掃描超限或出現兩個吻合 | needs_reconciliation，不能因找不到唯一紀錄重建。 |
| A32 | 公開 create 遇到 SDK 5xx／429 | 不隱藏重送；5xx 進核對，429 按期限及 Retry-After。 |
| A33 | 重複 interaction 或清理 receipt 後重送事件 | 不配置第二個手動名額，不多花生成額度。 |
| A34 | 午夜時 queued／sending／partial job | 不開始舊日新請求，已送效果核對；不補過去多日。 |
| A35 | 更改時區／發題時間／當日題數 | 生效時間明確，當日快照及唯一日期保持不變。 |
| A36 | 管理員同時更新、排隊後角色被撤銷 | 避免 lost update；執行時重新驗證並拒絕未授權工作。 |
| A37 | 題庫同步半途失敗／負 difficulty／NULL | 沿用完整舊快照，負值與缺值不混淆。 |
| A38 | 第一個生成訂閱者離開／大量等待者 | 不取消其他人的結果，佇列與等待者數量受限。 |
| A39 | 大量私人生成占用 LLM | 公開翻譯仍取得執行名額，所有請求遵守總 deadline。 |
| A40 | 空 choices／refusal／length／HTML／缺 finish reason | 拒絕不完整快取；缺 finish reason 僅明確配置例外可接受。 |
| A41 | Emoji、CJK、組合字元、超長範例行 | 保守長度不超限、字元完整、範例還原一致。 |
| A42 | LLM 刪除或重複公式 placeholder | 完整性驗證失敗，不發布改寫的公式。 |
| A43 | 原文／模型／prompt／parser 改版 | 新生成使用新 fingerprint，舊按鈕仍引用原題版本。 |
| A44 | 圖片 redirect 到內網、解壓炸彈或超大回應 | 在連線／讀取界限拒絕，沒有密鑰轉送或無界記憶體。 |
| A45 | 人工刪除／修改／鎖定貼文、必要權限撤銷 | 停止該公開工作，保留歷史，不擅自覆寫成員內容。 |
| A46 | Migration 失敗、schema 舊版、與 run 並行 | 明確拒絕啟動或操作，不自動刪庫修復。 |
| A47 | SIGTERM／DB 斷線／Singleton session 丟失 | 停止新工作，期限內關閉，後續啟動先核對。 |
| A48 | 還原比 Discord 狀態更舊的備份 | recovery mode 阻擋新公開工作，先核對不明確及孤立紀錄。 |

### 14.4 驗證方式與 Go 檢查

- 使用保存的 AtCoder／CF HTML 樣本，驗證一般題目、公式、圖片及多組範例解析。
- 單元測試涵蓋篩選、去重、時區日期、訊息分段、設定驗證及快取鍵。
- 使用模擬 Discord／LLM／HTTP 回應測試錯誤與恢復流程，不在一般測試中消耗真實 LLM 額度。
- 在測試伺服器進行 Forum 建立、長題目、ephemeral 回覆、權限與重啟按鈕驗證。
- 真實 API 連線測試由部署維護者配置後執行；與不需密鑰的自動測試分開。
- 預設值及 API schema 經使用者審核後再固定，不因本文件提前執行部署。

實作後的必要檢查：

- `gofmt`、`go vet ./...`、`go test ./...`；並發相關測試再執行 `go test -race ./...`。
- PostgreSQL 整合測試使用相同 major 的真實測試容器，驗證唯一／複合外鍵、claim、lease、交易取消、migration 與備份恢復；不能用 SQLite 假裝測過 PostgreSQL。
- `httptest` 模擬截斷 body、429、5xx、丟失成功回應、redirect、逾時與取消；捕捉實際 request 次數，驗證 SDK 無隱藏重送。
- Clock／RNG 注入測試日期、DST、設定版本與來源配額；DB 租約測試用 DB 時間，不能只修改 Go fake clock 就宣稱租約已到期。
- 在各 crash point 注入失敗：保存 operation 前後、HTTP 送出後、成功結果寫回前後；逐一驗證無盲目重送。
- 若固定 SDK 不支援必要參數或 context 取消，補 adapter 契約測試後才接受該版本。

上述為未來實作驗收清單，本次規格審查不執行或宣稱通過 runtime 測試。

## 15. 交付內容與實作順序

規格批准後，第一版交付：

- 可啟動的 Bot 程式與依賴定義。
- 不含密鑰的環境變數及設定範例。
- PostgreSQL schema、SQL migrations、初始化及版本管理。
- 來源抓取、題目解析、翻譯、提示、解答、按鈕、指令與排程。
- 必要自動測試及固定樣本。
- 本機／Docker Compose 啟動、Discord 邀請與權限、設定及故障處理文件。

建議順序：

1. 基礎設定、資料庫、來源 adapter 及題目解析。
2. 抽題、完整翻譯及訊息分段。
3. Forum 發布、基本指令及每日排程。
4. 提示／解答、私人回覆、生成快取及並發控制。
5. 重啟恢復、失敗通知與驗收。

不將正式 Discord 發題、外部部署或真實 LLM API 呼叫視為生成本規格所需的操作。

## 16. 選配功能審核表

所有項目預設為「待審核」，不自動納入第一版。

| 編號 | 功能 | 說明 | 狀態 |
| --- | --- | --- | --- |
| O-A | 三階段提示 | 輕提示、關鍵觀察、完整演算法方向；需保存每位使用者的提示進度。 | 待審核 |
| O-B | `/problem pick <url>` | 指定支援平台的官方題目 URL，套用相同解析、翻譯及按鈕流程。 | 待審核 |
| O-C | `/daily pause`、`resume` | 暫停／恢復排程，需定義暫停時手動發題及恢復補發行為。 | 待審核 |
| O-D | `/history` | 查詢過去日期、題目及討論串連結。 | 待審核 |
| O-E | 題型篩選 | 先支援 CF tags，指定或排除 DP、圖論等題型；不假設 AtCoder 有相同資料。 | 待審核 |
| O-F | 題目錯誤回報按鈕 | 回報翻譯缺漏、解析問題或疑似錯誤解答給管理員。 | 待審核 |
| O-G | `/usage` 與生成額度 | 查看使用量、設定每日請求／token 上限；價格由配置決定。 | 待審核 |
| O-H | 解題打卡 | 自行標記完成並計算連續天數，不宣稱已驗證 AC。 | 待審核 |
| O-I | 平台帳號綁定 | 綁定 AtCoder／CF handle，查詢 AC 紀錄；另定義驗證、輪詢及隱私設定。 | 待審核 |
| O-J | 官方題解輔助 | 能取得官方題解時作為生成依據，保留來源並定義抓取失敗處理。 | 待審核 |

## 17. 實作前待確認事項

| 編號 | 決策 | 草案提案 | 審核狀態 |
| --- | --- | --- | --- |
| D01 | 完成格式 | Chat Completions `/chat/completions`，而非舊式 `/completions`。 | 待確認 |
| D02 | 每日貼文結構 | 一天一篇，兩題共用討論串。 | 待確認 |
| D03 | 題目來源 | 每日 AtCoder、CF 各一題。 | 待確認 |
| D04 | 發題時間 | `Asia/Taipei` 每天 09:00。 | 待確認 |
| D05 | 難度及去重 | CF 800～1400、AtCoder 原始估計 difficulty 400～1000、90 天去重。 | 待確認 |
| D06 | 提示／解答可見性 | 僅點擊者可見。 | 待確認 |
| D07 | 技術方案 | Go 與 PostgreSQL 已指定；discordgo、pgx、Docker Compose 為架構提案。 | Go／PostgreSQL 已確認；其餘待確認 |
| D08 | 解答語言 | C++17 預設，允許設定 Python 3。 | 待確認 |
| D09 | LLM 服務與模型 | 使用者指定；允許三種任務分別設定模型。 | 待確認 |
| D10 | 選配範圍 | O-A～O-J 逐項選擇。 | 待確認 |

正式啟用時還需要提供目標伺服器／討論區、必要標籤、Discord Token、LLM API key、base URL、模型及部署位置。這些資料不需要以真實值寫入本規格。

## 18. 參考資料

以下來源用於確認平台介面及限制；本文件中的預設值與產品行為屬專案提案。

- [Discord：Forum／Media Channel 建立討論串](https://docs.discord.com/developers/resources/channel#start-thread-in-forum-or-media-channel)：Forum 貼文及起始訊息的 API 行為。
- [Discord：訊息資源](https://docs.discord.com/developers/resources/message)：訊息長度、附件及格式限制。
- [Discord：接收及回應互動](https://docs.discord.com/developers/interactions/receiving-and-responding)：初始回應期限、defer、ephemeral 及互動 token 有效期。
- [Codeforces：API 物件](https://codeforces.com/apiHelp/objects#Problem)：Problem metadata、rating 及比賽狀態欄位。
- [AtCoder Problems：API／資料集文件](https://github.com/kenkoooo/AtCoderProblems/blob/main/doc/api.md)：非官方題庫、難度估計及存取間隔。
- [OpenAI：建立 Chat Completion](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)：請求及回應格式、模型參數差異。
- [discordgo：官方專案與範例](https://github.com/bwmarrin/discordgo)：Go Discord SDK；使用前固定已驗證版本。
- [pgxpool：套件文件](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)：連線池及必須明確提交／回滾的交易。
- [PostgreSQL：SELECT 與 SKIP LOCKED](https://www.postgresql.org/docs/current/sql-select.html)：任務領取所用的列鎖語意。
- [PostgreSQL：Explicit Locking](https://www.postgresql.org/docs/current/explicit-locking.html)：session／transaction advisory lock 的生命週期。
- [Discord：Gateway](https://docs.discord.com/developers/events/gateway)：intent 與 Bot 自己發送內容的例外。
- [Discord：Components](https://docs.discord.com/developers/components/reference)：custom ID 及按鈕限制。
- [discordgo：REST 原始碼](https://github.com/bwmarrin/discordgo/blob/master/restapi.go)：request context、rate-limit 及 retry 選項；以固定版本再次驗證。
- [Go：net/http](https://pkg.go.dev/net/http)：context、redirect、HTTP client 的連線與回應生命週期。
- [goose：官方專案](https://github.com/pressly/goose)：SQL migrations 與工具管理的版本表。

## 19. 三輪對抗式審查紀錄

本節記錄對規格提出的反例、修正及預期驗證，不宣稱程式已實作或測試已通過。三輪按順序進行，後一輪審查前一輪修訂後的文件。

### 19.1 第一輪：需求語意與資料不變量

| 編號 | 嚴重度 | 反例／缺口 | 修正與驗證方向 |
| --- | --- | --- | --- |
| R1-01 | 高 | SQLite／Python 與使用者指定技術棧不符。 | 全文件改成 Go／PostgreSQL，密鑰與 DSN、資料持久化及依賴契約同步修正。 |
| R1-02 | 高 | `/problem random` 未指定來源，balanced 模式對單題沒有定義。 | 明確以當日手動名額交替來源，weighted 抽一次保存；測試並發接受與恢復不重抽。 |
| R1-03 | 高 | 手動貼文先建立後修改每日題數，排程可能補錯名額。 | 首次建立 daily 時凍結公開發題快照，每日與手動名額有獨立唯一鍵。 |
| R1-04 | 高 | 更新原文後，舊貼文按鈕可能生成另一版題目的解答。 | Immutable statement version 與外鍵固定發布版本，快取包含版本。 |
| R1-05 | 高 | CF `PROGRAMMING` 並不保證是一般非互動題。 | 加入平台解析器題型分類；不可靠題型拒絕發布並保留理由。 |
| R1-06 | 高 | 跨伺服器子紀錄或 statement／problem 關聯錯配，僅查 UUID 不能保證隔離。 | 複合外鍵、穩定題目鍵與帶 guild ID 的 repository 查詢。 |
| R1-07 | 中 | 90 天、奇數每日題數、難度缺值及題庫半更新沒有精確語意。 | 定義 UTC 窗口、日期日序、NULL 與負難度、完整快照切換；補固定邊界測試。 |
| R1-08 | 高 | 更改時區或管理員併發更新可造成重複每日紀錄／設定遺失。 | 生效週期、樂觀版本控制、每日唯一約束；第二輪繼續挑戰其排程與租約實作。 |

### 19.2 第二輪：故障、並發與外部一致性

| 編號 | 嚴重度 | 反例／缺口 | 修正與驗證方向 |
| --- | --- | --- | --- |
| R2-01 | 高 | 單副本只靠部署宣告，誤啟第二副本仍會重複發題。 | 獨立 session advisory lock、鎖連線失敗停止副作用；第二副本不得啟動 Gateway。 |
| R2-02 | 高 | Job row claim 成功但沒有伺服器鎖，可讓同一貼文兩個 worker 交錯。 | 同交易鎖 job 與 guild runtime；租約 epoch 條件更新；不同 guild 可並行。 |
| R2-03 | 高 | Discord 成功後 DB commit 失敗，直接重試造成重複貼文。 | Durable operation、`ambiguous` 分流、逐頁核對；只認領唯一證據，不因查不到而重送。 |
| R2-04 | 高 | session lock 經 pool／transaction proxy 漂移，pgx 取消後交易未回滾。 | 鎖使用專用 session；每個交易明確 commit／rollback，取消後仍有清理 deadline。 |
| R2-05 | 高 | 把私人互動與 durable job 綁在一起，重啟後 token 不在卻假裝可續送。 | 公開工作與私人投遞分開；token 僅存在有期限記憶體，重新點擊取得已保存結果。 |
| R2-06 | 高 | 第一位使用者離開會取消所有人的共享生成，反之等待者無限增長。 | 共用生成 context、獨立訂閱期限、等待者與不同 key 雙重上限、公私佇列公平性。 |
| R2-07 | 高 | 重複 interaction、午夜與自動重試新增第二個手動名額或舊日內容。 | 長期 job dedup key、名額唯一鍵、當日 ends_at 及持久化候選次數。 |
| R2-08 | 中 | 回報不明確卻沒有可操作的恢復方法，整個 guild 永久停住。 | 唯讀 reconcile 報告、可驗證認領與需明確確認的重送決定，保存審計紀錄。 |

### 19.3 第三輪：介面、資源界限與部署可驗證性

| 編號 | 嚴重度 | 反例／缺口 | 修正與驗證方向 |
| --- | --- | --- | --- |
| R3-01 | 高 | SDK 預設重試繞過第二輪的 durable operation，5xx 可能重複發送。 | 關閉 create 的隱藏重試、取消可控 bucket 等待、明確 raw JSON 補足必要欄位；A32。 |
| R3-02 | 高 | Go len 計算 bytes，Emoji 長度／範例分段可能超限或破壞原文。 | 保守 UTF-16 計數、逐行可還原分段、全題預渲染驗證；A41。 |
| R3-03 | 高 | 有文字不代表完整解答；length、refusal 或缺少 choice 也可能被快取。 | 驗證 finish reason、回應結構與總 context；相容例外明示；A40。 |
| R3-04 | 高 | 翻譯保留章節但改掉公式，頁面圖片可導向內網或超大解壓內容。 | Protected atoms、樣例由程式保留、抓取 host／位址與解壓上限；A42、A44。 |
| R3-05 | 高 | migrate／人工 reconcile 與 run 各鎖不同 key，可邊發題邊改 schema／進度。 | 命令共用 singleton namespace、顯式 migration 與停機寫入恢復；A46。 |
| R3-06 | 高 | 前輪 execution deadline 在 retry_wait 已到期，查詢永遠無法 claim；其他 job 超越半題或 manual 失敗污染 daily completed。 | 區分待開始／執行期限、resume pointer、全域 admission 短交易及每日配額推導；A27、A34、A08、A14。 |
| R3-07 | 中 | Docker 沒有 tzdata／CA、DB 還原落後 Discord、關機後失去租約卻仍發送。 | 明確初始化、健康／degraded、graceful shutdown 及 recovery mode；A47、A48。 |
| R3-08 | 高 | 權限只在接受時檢查；偽造舊按鈕或排隊後撤銷角色仍可觸發公開工作。 | Router 檢查固定 message／thread／guild，執行前重驗證權限，FK 補完整同日關聯；A21、A36。 |

### 19.4 審查後結論與剩餘限制

三輪共提出 24 個規格／架構缺口，已在文件中補上對應設計及驗收方式。Go／PostgreSQL 已確認；其餘產品預設值與 O-A～O-J 選配仍待使用者審核。

- Discord 與 PostgreSQL 不能跨系統原子提交，設計採持久化發布意圖、核對及不明確時停止，不承諾 exactly-once 外部發送。
- LLM 結果即使格式完整仍可能語意錯誤；第一版提供原題連結與 AI 標示，不承諾 AC 或形式化正確性。
- 外部來源解析及相容供應商仍需用實際樣本／固定 SDK 版本驗證；本文件不等同已完成連線測試。
- 關鍵公式、必要圖片或長度無法完整處理時改抽其他題；不會把未支援內容偽裝成全文。
- 本輪完成的是規格與架構審查，尚未產生 Bot 程式、migration、部署資源或執行付費 API。
