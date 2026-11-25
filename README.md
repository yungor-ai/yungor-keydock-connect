# KeyDock Connect · KeyDock 接入助手

Windows x64 原生桌面客户端。员工填写 KeyDock 服务地址和分配给本机的设备接入密钥，助手会领取个人模型配置和 SS 订阅，更新 Codex，并在窗口中显示测试结果。

在 KeyDock 服务地址后加上 `/software`，打开「软件下载」，选择 **KeyDock Connect**。也可以从[公开发布页下载 Windows x64 安装包、免安装 ZIP 和 SHA-256 校验文件](https://github.com/yungor-ai/yungor-keydock-connect/releases/latest)。已运行旧版时，先从右下角托盘退出助手，再运行新版安装包。

## 首次接入

1. 管理员在 KeyDock 的「设备」详情中签发 **Device Key**，将 HTTPS 服务地址和设备密钥交给员工。服务端需要支持 `POST /api/connect/v1/device`。早期的 KeyDock v1.9.0 缺少此接口。
2. 安装并打开 **KeyDock 接入助手**。填写上述两项，点击 **连接并测试**。服务器地址示例为 `https://keydock.example.com`。助手会自动获取分配给设备的模型名称、模型 Key 和可用的代理订阅。
3. 查看窗口里的分项结果。助手会核对 Codex 实际使用的本地配置，向配置的模型发出一条简短请求，并用临时代理核心验证 SS 节点的 HTTPS 连通性。模型测试可能产生少量用量。仅开通模型的设备会显示跳过 SS 检测。
4. 需要使用 Codex 时，打开官方 Windows 桌面版或在新终端运行 `codex`。已有 Codex 进程请完全退出再打开。客户端需支持 Responses API 与命令凭据助手。需要应用流量经过代理时，在接入助手中选择并启用代理模式。

后续可直接使用同一台电脑保存的设备授权重新同步。管理员轮换设备接入密钥或更换服务器地址后，请填写对应的新密钥。个人模型 Key 轮换后，再同步即可更新 Codex。[Codex 安装指南](https://developers.openai.com/codex/quickstart/) · [KeyDock 服务端设备接入说明](https://github.com/yungor-ai/yungor-keydock/blob/main/docs/device-connect.md)

服务端未提供模型时，接入助手默认使用 `gpt-6.2-sol`。连接后以服务端明确下发的模型为准。

## 代理模式和订阅刷新

- **系统代理**是默认模式。使用 Windows 系统代理设置的应用按订阅规则分流，普通 Windows 用户可在接入设备并取得 SS 订阅后启用。
- **TUN（虚拟网卡）**通过 Windows 路由接管流量，可用于复测 Codex 等应用中绕过系统代理的请求。请由管理员校验 `KeyDockConnect-windows-x64.zip`，将完整内容解压到 `%ProgramFiles%\KeyDock Connect`，保留 Program Files 默认权限，确认同一目录下有 `KeyDockConnect.exe` 和随包附带的 `core` 目录。先从托盘退出当前实例，再用原 Windows 账号右键该目录中的 `KeyDockConnect.exe`，选择「以管理员身份运行」，然后选择并启用 TUN。启用后，从 Windows 开始菜单正常打开 Codex。若 Windows 已开启其他手动代理或代理设置脚本，请先在原工具中关闭。
- 切换模式前先关闭当前模式。启用所选模式后，可在应用中复测实际请求，例如在 Codex 中重新试一次图片操作。
- 启用代理时立即获取订阅。代理运行期间每小时检查一次，窗口收至托盘后也继续。配置有变化时会重载代理内核，现有连接可能短暂中断。
- **最近更新时间**以 Windows 本地时间显示到秒。成功启用代理或刷新订阅后更新时间，订阅内容相同也记录这次成功检查。当前订阅首次成功前显示「尚未更新」。关闭代理或重启助手后保留上次成功时间，刷新失败时另有提示。订阅下载或校验失败时继续使用原配置。

## 代理连通失败

「代理连通」失败时，在「连接检查」下方查看排查提示。检测到本机时钟比 KeyDock 服务端快或慢超过 30 秒时，助手会指出偏差方向。点击 **打开时间设置**，在 Windows 的「日期和时间」页面点 **立即同步**，再点 **连接并测试**。以管理员身份运行助手时，从开始菜单打开 Windows 设置。企业托管电脑请沿用管理员指定的时间源。

如果 Clash 等其他代理工具同时启用了 TUN（虚拟网卡），可在对应工具里暂时关闭 TUN 或退出该工具，再复测。仍然失败时，导出诊断日志交给管理员。

## 导出诊断日志

遇到连接错误时，点击 **连接并测试** 复现一次，然后在「连接检查」区点击 **导出诊断日志**。选择保存位置后，将生成的 JSON 文件交给管理员。

诊断文件包含客户端和 Windows 版本、当前连接状态，以及最近 200 条设备授权、Codex 配置、模型和代理检查事件。代理检查记录订阅获取、内核启动、本机代理连接、TLS 和 HTTPS 响应等阶段。文件只使用固定事件名称、耗时、错误分类和 HTTP 状态码等字段，排除设备密钥、API Key、订阅地址和配置内容。诊断事件保存在本机，导出后由员工自行分享。

## 托盘与恢复

- 关闭窗口后，助手在 Windows 通知区域继续运行。通过右下角托盘图标重新打开窗口，或选择退出。Windows 可能将图标收在「显示隐藏的图标」中。
- 退出助手会关闭它启动的代理。系统代理模式会恢复它接管的 Windows 用户代理设置，TUN 模式会清理它创建的路由。再次启用 TUN 时，可复用已停止转发的 `KeyDockTun` 网卡。若提示网卡未空闲或发生名称冲突，请重启 Windows 后复测。提示持续时，请联系管理员检查该网卡。
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

在已安装 Inno Setup 的 Windows 电脑上，从仓库根目录运行 `./packaging/build-installer.ps1`。Windows CI 配置了系统代理测试、TUN 路由恢复检查和安装包验收。正式安装包和 SHA-256 校验文件随 GitHub Release 发布。代理内核、离线分流数据和第三方许可包含在安装包中。
