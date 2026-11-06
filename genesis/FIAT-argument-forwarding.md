# 把 -- 之後的參數原封不動傳給 claude

目前 ccrun 啟動 Claude Code 時，只會帶 `--remote-control`、`--name` 和第一句提示詞。有些 session 需要額外的 claude 參數才能啟動，例如自己寫的 MCP channel 要用 `--dangerously-load-development-channels`，或想指定 `--model`、`--continue`，現在都沒辦法透過 ccrun 帶進去

我希望 `--` 之後的參數，全部原原本本接在 claude 的參數後面

```
ccrun -d <dir> -n mybot "it is 8:30" -- --dangerously-load-development-channels server:webhook
ccrun -d <dir> -- --model opus --continue
```

- `--` 之前照舊：ccrun 自己的選項與提示詞，選項和提示詞可以交錯
- `--` 之後的每個參數都不解析、不改寫、不重新斷詞，照順序傳給 claude，包括含空白或引號的參數
- 只在 ccrun 啟動新 session 時有效；`-p`、`-n` 注入、`-s`、`-q`、`-l` 等不會啟動 session 的用法若帶了 `--` 參數，要報錯說明，不要默默忽略
- 目前 `--` 的意思是「之後的都當提示詞」，這個意思要改掉；提示詞一律放在 `--` 之前。usage、README、測試一起更新
- 傳給 claude 的參數若和 ccrun 自己帶的（`--remote-control`、`--name`）重複，以 ccrun 自己的為準還是報錯，請選一個並寫進 README

## 驗收

- 單元測試涵蓋：`--` 前後的切分、提示詞與選項交錯、空的 `--`、含空白的參數
- 實際啟動一次：帶 `-- --model sonnet`，確認啟動的 claude 真的用那個模型，然後用 `-q` 關掉
