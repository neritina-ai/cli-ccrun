# 以關鍵字查詢 session 的 PID

目前 ccrun 無法查詢特定已開啟 session 的 PID。要加一個功能讓使用者查詢 PID，這樣後續才能 `ccrun -p PID` 對它注入命令。

用關鍵字部分比對，比對都符合時則列出多個。使用者自己要下正確的關鍵字來「只查詢到一個」。

```
ccrun -l [keyword ...]
```

- 不給關鍵字時列出所有執行中的 session
- 關鍵字不分大小寫，比對 session 名稱、工作目錄、session ID（claude.ai/code 網址裡的 `session_...` 與本機 `--resume` 用的那個）
- 多個關鍵字須全部符合，用來縮小範圍
- 一行一個 session，PID 在最前面；標題列走 stderr
- 沒有符合時 exit 1
