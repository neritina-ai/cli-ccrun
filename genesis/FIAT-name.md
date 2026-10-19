# 以 session 名稱建立並指定 session

我會有一個長期執行的 session，半途可能會被用 `claude --resume` 重啟，PID 變了但 session name 不變。所以需要 by name 的功能：雖然不知道它的 PID，但我知道它的 session name，也可以注入。

有些方便性，但也會有非唯一的問題。而且名稱可能全是數字，例如操作基金的 session 就叫 `00919`，不能讓 `-p` 兼收 PID 與名稱。

要讓使用者用「可控制的名稱」來建立 session，然後就用同一個名稱去注入。建立時可控制、且 `--resume` 也不會變的是哪個名稱，就用那個名稱建立、也用那個名稱注入。這樣使用者才能不靠查詢、而是靠自己指定來控制 session，例如自己控制 name1、name2、name3，而不是啟動後才去查「被系統命名成什麼了」。

工作排程一律用 `-d` 和 `-n` 呼叫，例如「請起來做事」。如果有人不小心把這個 session 關掉了，排程起來時就開一個新的 session 在這個工作目錄；如果本來就開著，就對原來那個 session 注入提示詞。安全上沒有問題：唯一的工作目錄配唯一的 session，不管它是被關掉又叫起來，還是本來就在，注入的都是我們本來就要它做事的那個 session，只要工作目錄符合就好。所以永遠用 `-d -n` 呼叫，由 ccrun 自動判斷要起新 session，還是用已經開著的。

## 調查結果

| | 建立時可指定 | 本機可讀（ccrun 找得到） | `--resume` 後 |
|---|---|---|---|
| session name（`claude --name`） | 是 | 是（session 紀錄的 name） | 不變 |
| Remote Control 名稱（`claude --remote-control <name>`） | 是 | 否，只在 claude.ai 伺服器端 | 不變 |
| 都不給時系統取的名稱 | 否 | 是 | 會變 |

Remote Control 清單顯示的是 Remote Control 名稱，沒給時顯示 session name。`/rename` 也能替執行中的 session 設定 session name。

## 決定

```
ccrun -d <working-dir> [-n <name>] [prompt]    開新的，或對已開著的注入
ccrun -n <name> <prompt> | - | -s | -q
ccrun -p <pid>  <prompt> | - | -s | -q
```

- `-n` 就是 session name：搭配 `-d` 是替新 session 命名，不搭配 `-d` 是指定執行中的 session
- 建立時 session name 與 Remote Control 名稱設成同一個，手機上看到的就是 `-n` 用的名稱；省略時用工作目錄名稱
- `-d` 遇到同名、同工作目錄的執行中 session，就不另開，把提示詞注入給它，並照樣印出它的 PID；沒有才開新的。工作目錄比對不分大小寫、不計結尾的斜線
- 同名但在別的工作目錄，或同名的不只一個，就拒絕、什麼都不開，所以自己發的名稱永遠只對到一個
- 省略 `-n` 時的預設名稱（工作目錄名）也照同一套規則
- 名稱須完整相同（不分大小寫），不做部分比對；同名多個（手動開的 session 仍可能發生）時拒絕並列出 PID
- 建立時等到 session 以該名稱登記好才返回，啟動後立刻 `-n` 也找得到
- 廢除 `-r`：建立與指定都用 `-n` 一個名稱，不另設第二種；ccrun 只有我在用，不需與舊版相容
