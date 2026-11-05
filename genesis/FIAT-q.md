# 讓 -q 在各種情境皆為有效選項

目前版本，執行多個注入後結束，使用者可能是這樣下指令

ccrun -d <dir> "請執行 FIATLUX.md"
ccrun -p <PID> "請再執行 FIAT-more.md"
ccrun -p -q

我希望 -q 在各種情境都可以是有效的選項，如此一來，使用者可以這樣使用

ccrun -d <dir> "請執行 FIATLUX.md"
ccrun -p <PID> "請再執行 FIAT-more.md" -q

可以節省一次呼叫

或

ccrun -d <dir> "請執行 FIATLUX.md" -q

用在一次性的執行，執行完即關閉 Claude

注意，這種一次性的執行，有可能執行好幾天才結束，也有可能是在等背景程式跑完，所以妳的 -q 不可以把它打斷

---

以下指示不屬於 FIAT 內容，如要 commit 請先刪除

首先，如果目前版本的 -q 本來就可以這樣用的話，則整個 FIAT 都不必做了，此檔案也先不必刪除，我拿去給 cli-cdrun 參考

我有觀察到，目前版本如果 Claude 正在執行，外界又下一個 ccrun -q 指令的話，程式會等所有的 turn 都結束了，才真正把 Claude 關掉，非常 graceful。我覺得只要將這個「graceful quit」重複利用，這個任務應該就完成了吧