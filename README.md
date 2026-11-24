# KeyDock Connect · KeyDock 接入助手

Windows x64 员工客户端。从 KeyDock 的加密个人接入包配置官方 Codex，按需为当前 Windows 用户开启系统代理。

## 员工使用

1. 运行 Windows 安装程序，再从开始菜单打开 **KeyDock 接入助手**。程序优先使用 Edge 的独立窗口，也支持默认浏览器。窗口连接到本机回环地址。
2. 选择管理员发放的加密个人 ZIP 或包含它的软件 ZIP，输入单独发放的口令。程序兼容旧版单 Key 包和新版多 Key 包；新版按清单选取这台设备的默认 Key。
3. 核对默认模型。接入包没指定模型时，点击 **读取模型** 选择网关返回的型号，也可以输入管理员提供的模型名。
4. 点击 **配置并打开 Codex**。首次安装 Codex 可通过窗口内的官方安装指南获取。Codex 已经在运行时，请完全退出后重开。使用原生 Windows CLI 的员工可以选择 **仅应用配置**，再到终端运行 `codex`。WSL 使用自己的配置目录。
5. 需要浏览器等应用经过公司代理时，点击 **启用系统代理**。关闭代理或使用 **退出接入助手** 会回收本程序的设置。直接关窗口时后台进程继续运行，可从开始菜单重新打开。

**恢复原 Codex 配置** 会撤销受管的提供商和模型设置。应用保存当前用户的加密备份，保留已有提供商和 `auth.json`。卸载前请先在窗口内退出助手，卸载程序会调用同一个恢复流程。启用系统代理仅影响采用 Windows 用户代理设置的应用。客户端采用订阅下发的 SS2022 规则分流，默认关闭 TUN 和局域网共享，断电等异常终止后在下次启动时处理遗留代理设置。

## 与 KeyDock 的连接

模型请求由 Codex 发往设备包中的 HTTPS 网关。凭据助手从 Windows DPAPI 保护的用户状态中取得个人 Key。设备包口令只用于当次导入。订阅按需从个人 HTTPS 地址直接刷新，代理内核、离线分流数据和第三方许可包含在安装包中。无需另装 CC Switch、Clash Verge Rev 或解压工具。

管理员可沿用 KeyDock 已有的加密交付包。员工只收到 API 信息或 Excel 时，需要管理员另外提供该设备的加密包。Codex 客户端需支持 Responses API 和自定义提供商的命令凭据助手。模型网关按 KeyDock 管理员授权计费；读取模型只请求模型列表，真实对话在官方 Codex 中发起。[Codex 网关指南](https://learn.chatgpt.com/docs/enterprise/connect-to-a-gateway)

## 开发与构建

使用 go.mod 指定的 Go 工具链。测试使用临时目录和合成设备资料。

```sh
go test -race ./...
go vet ./...
mkdir -p dist/app
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-H=windowsgui -s -w" -o dist/app/KeyDockConnect.exe ./cmd/keydock-connect
python3 scripts/package-core.py dist/app
```

在 Windows 上从仓库根目录运行 `ISCC.exe packaging/keydock-connect.iss` 生成按用户安装的安装包。安装目录中的凭据助手需要保留在固定位置。GitHub Actions 使用 Windows runner 测试并生成安装程序与 SHA-256，产物可在对应工作流运行的 Artifacts 中下载。

Go 入口位于 `cmd/keydock-connect`。设备包、Codex 配置、系统代理和本地界面分别位于 `internal/bundle`、`internal/codex`、`internal/proxy`、`internal/desktop`。第三方代理内核与规则数据的版本、下载地址和哈希记录在 `scripts/package-core.py`，随附来源和许可位于安装目录的 `licenses`。
