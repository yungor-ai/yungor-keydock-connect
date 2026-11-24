# KeyDock Connect · KeyDock 接入助手

Windows x64 原生桌面客户端。员工填写 KeyDock 服务地址和分配给本机的设备接入密钥，助手会领取个人模型配置和 SS 订阅，更新 Codex，并在窗口中显示测试结果。

在 KeyDock 服务地址后加上 `/software`，打开「软件下载」，选择 **KeyDock Connect**。也可以从[公开发布页下载 Windows x64 安装包和 SHA-256 校验文件](https://github.com/yungor-ai/yungor-keydock-connect/releases/latest)。已运行旧版时，先从右下角托盘退出助手，再运行新版安装包。

## 首次接入

1. 管理员在 KeyDock 的「设备」详情中签发 **Device Key**，将 HTTPS 服务地址和设备密钥交给员工。服务端需要支持 `POST /api/connect/v1/device`。早期的 KeyDock v1.9.0 缺少此接口。
2. 安装并打开 **KeyDock 接入助手**。填写上述两项，点击 **连接并测试**。服务器地址示例为 `https://keydock.example.com`。助手会自动获取分配给设备的模型名称、模型 Key 和可用的代理订阅。
3. 查看窗口里的分项结果。助手会核对 Codex 实际使用的本地配置，向配置的模型发出一条简短请求，并通过临时代理核心验证 SS 节点。模型测试可能产生少量用量。仅开通模型的设备会显示跳过 SS 检测。
4. 需要使用 Codex 时，打开官方 Windows 桌面版或在新终端运行 `codex`。已有 Codex 进程请完全退出再打开。客户端需支持 Responses API 与命令凭据助手。需要浏览器等应用经过公司代理时，再开启 **系统代理**。

后续可直接使用同一台电脑保存的设备授权重新同步。管理员轮换设备接入密钥或更换服务器地址后，请填写对应的新密钥。个人模型 Key 轮换后，再同步即可更新 Codex。[Codex 安装指南](https://developers.openai.com/codex/quickstart/) · [KeyDock 服务端设备接入说明](https://github.com/yungor-ai/yungor-keydock/blob/main/docs/device-connect.md)

## 托盘与恢复

- 关闭窗口后，助手在 Windows 通知区域继续运行。通过右下角托盘图标重新打开窗口，或选择退出。Windows 可能将图标收在「显示隐藏的图标」中。
- 退出助手会恢复它接管的 Windows 用户代理设置。系统或助手被强制终止时，下次启动会尝试恢复。系统代理按订阅规则分流，关闭 TUN 和局域网共享。
- **恢复原 Codex 配置** 会撤销助手管理的提供商和模型设置。助手保留其他提供商、现有登录凭据以及后续添加的项目设置。卸载前请从托盘退出，卸载程序会调用恢复流程。
- 设备密钥和服务端返回的个人资料由 Windows 当前用户的 DPAPI 加密保存。升级自 v0.1 时保留已经应用的 Codex 配置及其恢复记录；首次在线同步需要输入服务地址和设备密钥。

## 构建

使用 go.mod 指定的 Go 工具链。原生窗口使用 Walk 和 Win32 系统控件。设备连接、模型测试、代理测试都有独立的合成数据回归。

```sh
go test -race ./...
go vet ./...
mkdir -p dist/app
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-H=windowsgui -s -w" -o dist/app/KeyDockConnect.exe ./cmd/keydock-connect
python3 scripts/package-core.py dist/app
```

在已安装 Inno Setup 的 Windows 电脑上，从仓库根目录运行 `./packaging/build-installer.ps1`。CI 在 Windows runner 上执行系统代理和安装验收。正式安装包和 SHA-256 校验文件随 GitHub Release 发布。代理内核、离线分流数据和第三方许可包含在安装包中。
