# 啟動並控制 Claude Code 的 CLI

我想建立一個 ccrun 的 CLI 讓第三方工具如 OpenClaw 或 skill 能啟動 Claude Code，而我再從 remote control 與它互動，最後要能關閉它。有些 skill 的套裝指令則需要注入提示詞

預定使用方式為

```
ccrun -d <working-dir> [-r <remote-name>] [prompt]
```

- 若 `<working-dir>` 不存在則建立
- remote-name 供使用者遠端操控時辨識用，可省略，省略時就用工作目錄的名稱
- prompt 可輸入第一句提示詞
- 由 stdout 得知 PID
- 啟動的視窗預設最小化，使用者回到電腦前可從工作列還原接手

```
ccrun -p <PID> <prompt>
ccrun -p <PID> -
```

- 注入提示詞，其中 `-` 代表從 stdin 注入

```
ccrun -p <PID> -q
```

- 結束離開

請參考 D:\projects\self-compact 注入提示詞的程式片段

我希望安裝方式也是透過 `go install <GitHub 的網址>` 安裝成 .exe 檔