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