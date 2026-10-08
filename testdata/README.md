# Parser fixtures

`atcoder.html` 與 `codeforces.html` 是精簡的結構／空白／公式測試樣本。

`atcoder-official.html`、`codeforces-official.html` 是 2026-10-09 由公開官方題目頁抓取的固定回歸樣本：

- https://atcoder.jp/contests/abc100/tasks/abc100_a
- https://codeforces.com/problemset/problem/4/A?locale=en

原始頁面包含平台的原文與網站內容，只作來源相容性測試。來源作品權利屬原作者／平台；Bot 發布時保留原站連結與 AI 翻譯標示。

`LIVE_SOURCE_TEST=1` 才連線重新驗證來源，預設測試只讀固定樣本。更新固定樣本須明確設定 `LIVE_SOURCE_OUTPUT_DIR`，避免一般測試改動 repository。
