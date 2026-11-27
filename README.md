# KeyDock Connect · KeyDock 接入助手

Windows x64 和 macOS Apple Silicon 原生桌面客户端。员工填写 KeyDock 服务地址和分配给本机的设备接入密钥，助手会领取个人模型配置和 SS 订阅，更新 Codex，并在窗口中显示测试结果。

Windows 安装包可在 KeyDock 服务的 `/software` 下载。安装需要管理员批准，程序会安装到 `%ProgramFiles%\KeyDock Connect`。安装完成后，从 Windows 开始菜单打开 **KeyDock Connect**。跨平台版本的 Windows 安装包、免安装 ZIP 和 Apple Silicon DMG 会在同一个 GitHub Release 提供。[公开发布页](https://github.com/yungor-ai/yungor-keydock-connect/releases/latest)

## 首次接入

1. 管理员在 KeyDock 的「设备」详情中签发 **Device Key**，将 HTTPS 服务地址和设备密钥交给员工。服务端需要支持 `POST /api/connect/v1/device`。早期的 KeyDock v1.9.0 缺少此接口。
2. 安装并打开 **KeyDock 接入助手**。在「设备接入」填写上述两项，点击 **连接并测试**。服务器地址示例为 `https://keydock.example.com`。助手会自动获取分配给设备的模型名称、模型 Key 和可用的代理订阅。
3. 在「连接检查」的「设备接入检测」查看分项结果。助手会核对 Codex 实际使用的本地配置，向配置的模型发出一条简短请求，并用临时代理核心验证 SS 节点的 HTTPS 连通性。模型测试可能产生少量用量。仅开通模型的设备会显示跳过 SS 检测。
4. 需要使用 Codex 时，打开已安装的桌面版或在新终端运行 `codex`。已有 Codex 进程请完全退出再打开。客户端需支持 Responses API 与命令凭据助手。需要让应用流量经过代理时，按相应平台的说明启用。

「网络访问检测」显示 Google 连通性，以及当前网络路径的公网 IPv4 与 KeyDock 服务端 IPv4 的比较结果。如果其中一端没有查到 IPv4，窗口会提示原因。切换网络后可点击 **重新检测网络**。

后续可直接使用同一台电脑保存的设备授权重新同步。管理员轮换设备接入密钥或更换服务器地址后，请填写对应的新密钥。个人模型 Key 轮换后，再同步即可更新 Codex。[Codex 安装指南](https://developers.openai.com/codex/quickstart/) · [KeyDock 服务端设备接入说明](https://github.com/yungor-ai/yungor-keydock/blob/main/docs/device-connect.md)

服务端未提供模型时，接入助手默认使用 `gpt-6.2-sol`。连接后以服务端明确下发的模型为准。

## 检查更新

填写 KeyDock 服务地址后，点击窗口底部的 **检查更新**。找到新版时，选择 **在线更新**，助手会核验 GitHub 官方发布的摘要，并从该 KeyDock 服务下载安装包。检查和下载使用所填的 HTTPS 地址，无需填写 Device Key。底部的 **软件下载** 可直接打开该地址对应的软件下载页。

安装前，助手会关闭自己启动的代理。Windows 会打开安装程序，按系统提示批准并完成安装。若从 Windows 免安装版切换到安装版，请在新版里点一次 **连接并测试**，更新 Codex 的凭据助手路径。macOS 会替换已安装的应用并重新打开，当前账号需要能写入应用所在目录。设备授权会继续使用。

## macOS Apple Silicon

1. 在运行 macOS 13 或更新版本的 Apple Silicon Mac 上，使用正式签名并经 Apple 公证的 `KeyDockConnect-macos-arm64.dmg`。将 **KeyDock Connect** 拖到「应用程序」，再从「应用程序」打开。
2. 填写设备授权并点击 **连接并测试**。设备资料及 Codex 恢复记录在本机加密保存，加密密钥由当前 macOS 用户的钥匙串保护。移动应用后重新连接，以便更新 Codex 的凭据助手路径。
3. 已分配 SS 订阅的设备可以在「网络代理」选择代理模式。首次启用时，如果 macOS 提示授权，请在弹出的系统设置中允许 KeyDock Connect 在后台运行，再回到助手点击启用。
   - **系统代理**。点击 **启用系统代理**，助手会为当前网络自动设置 HTTP、HTTPS 和 SOCKS 代理。关闭系统代理或退出助手时会恢复原设置。
   - **TUN（虚拟网卡）**。选择该模式并点击 **启用 TUN**。TUN 通过 IPv4 虚拟网卡按订阅规则分流，沿用系统当前的 DNS 配置。如果其他 VPN 或 TUN 已接管互联网路由，助手会阻止启用。请先在原工具中断开连接，再重试。
4. 选择另一种代理模式后，点击右侧按钮确认切换。关闭窗口后代理继续运行，可从菜单栏重新打开助手。点击 **关闭 TUN** 或退出接入助手时，助手会清理 TUN 接管的路由。可从菜单栏选择 **退出接入助手**，或在窗口中按 `⌘Q` 退出。
5. 遇到连接错误时，可导出诊断日志。移除接入助手前，可在窗口底部的「更多操作」中选择 **恢复原 Codex 配置**。

升级后若仍无法连接 TUN 后台服务，先检查系统设置是否允许 KeyDock Connect 在后台运行。若已允许，请将诊断日志交给管理员。后台组件更新后可能需要重新注册。若确认遇到此情况，请协调这台 Mac 上的所有用户停用 KeyDock TUN 并退出助手。随后由发布维护人员在签名维护版本中调用 `SMAppService` 注销 KeyDock 的旧注册，等待完成后再注册。[Apple 后台组件更新说明](https://developer.apple.com/forums/thread/783539)

## Windows 代理模式和订阅刷新

- **系统代理**是默认模式。使用 Windows 系统代理设置的应用按订阅规则分流，普通 Windows 用户可在接入设备并取得 SS 订阅后启用。
- **TUN（虚拟网卡）**通过 Windows 路由接管 IPv4 流量，沿用系统当前的 DNS。TUN 运行期间暂停 IPv6 连接，避免流量绕开虚拟网卡。使用 `KeyDockConnect-Setup-windows-x64.exe` 安装后，从托盘退出助手，在开始菜单搜索 **KeyDock Connect**，右键选择「更多」→「以管理员身份运行」，并使用当前 Windows 账号。随后选择 **TUN（虚拟网卡）** 并点击 **启用 TUN**。若 Windows 已开启其他手动代理或代理设置脚本，请先在原工具中关闭。
- 选择代理模式后，点击右侧按钮应用。启用所选模式后，可在应用中复测实际请求，例如在 Codex 中重新试一次图片操作。
- 启用代理时立即获取订阅。代理运行期间每小时检查一次，窗口收至托盘后也继续。配置有变化时会重载代理内核，现有连接可能短暂中断。
- **最近更新时间**以 Windows 本地时间显示到秒。成功启用代理或刷新订阅后更新时间，订阅内容相同也记录这次成功检查。当前订阅首次成功前显示「尚未更新」。关闭代理或重启助手后保留上次成功时间，刷新失败时另有提示。订阅下载或校验失败时继续使用原配置。

## Windows 代理连通失败

「代理连通」失败时，在该项下方查看排查提示。检测到本机时钟比 KeyDock 服务端快或慢超过 30 秒时，助手会指出偏差方向。点击 **打开时间设置**，在 Windows 的「日期和时间」页面点 **立即同步**，再点 **连接并测试**。以管理员身份运行助手时，从开始菜单打开 Windows 设置。企业托管电脑请沿用管理员指定的时间源。

如果 Clash 等其他代理工具同时启用了 TUN（虚拟网卡），可在对应工具里暂时关闭 TUN 或退出该工具，再复测。仍然失败时，导出诊断日志交给管理员。

## 导出诊断日志

遇到连接错误时，点击 **连接并测试** 复现一次，然后在「连接检查」区点击 **导出诊断日志**。选择保存位置后，将生成的 JSON 文件交给管理员。

诊断文件包含客户端和操作系统版本、当前连接状态，以及最近 200 条设备授权、Codex 配置、模型和代理检查事件。代理检查记录订阅获取、内核启动、本机代理连接、TLS 和 HTTPS 响应等阶段。文件只使用固定事件名称、耗时、错误分类和 HTTP 状态码等字段，排除设备密钥、API Key、订阅地址和配置内容。诊断事件保存在本机，导出后由员工自行分享。

## Windows 托盘与恢复

- 托盘可用时，关闭窗口后助手继续运行，可通过右下角托盘图标重新打开窗口或退出。Windows 可能将图标收在「显示隐藏的图标」中。托盘暂不可用时，关闭窗口会退出助手并清理它启动的代理。
- 退出助手会关闭它启动的代理。系统代理模式会恢复它接管的 Windows 用户代理设置，TUN 模式会清理它创建的路由。再次启用 TUN 时，可复用已停止转发的 `KeyDockTun` 网卡。若提示网卡未空闲或发生名称冲突，请重启 Windows 后复测。提示持续时，请联系管理员检查该网卡。
- **恢复原 Codex 配置** 会撤销助手管理的提供商和模型设置。助手保留其他提供商、现有登录凭据以及后续添加的项目设置。卸载前请从托盘退出，卸载程序会恢复当前管理员账号的配置。多账号共用时，请先在每个账号内恢复原 Codex 配置并退出助手，再由管理员登录自己的 Windows 账号卸载。
- 设备密钥和服务端返回的个人资料由 Windows 当前用户的 DPAPI 加密保存。从免安装版改用正式安装包时，先退出旧副本，从开始菜单打开新版助手，再点击 **连接并测试**，让 Codex 使用新位置的凭据助手。
- 从旧版用户目录安装迁移时，在原 Windows 账号中退出托盘里的助手并通过 Windows「设置」→「应用」卸载旧版，再双击新版安装包，在 Windows 提示时批准安装。旧版卸载会恢复原 Codex 配置并清除已保存的设备授权，请准备好设备密钥。已装在 Program Files 的版本可退出后直接升级。

## 构建

使用 `go.mod` 指定的 Go 工具链。设备连接、模型测试、代理测试都有独立的合成数据回归。

### macOS

在 Apple Silicon Mac 上安装 Go、Python 3 和 Xcode 命令行工具，在仓库根目录运行以下命令。

```sh
go vet ./...
bash packaging/build-macos.sh
KEYDOCK_TEST_CORE="$PWD/dist/macos/KeyDock Connect.app/Contents/Resources/core/mihomo" go test -race ./...
cd dist/macos
shasum -a 256 -c SHA256SUMS.txt
```

构建产物包含原生 `.app`、DMG 和 SHA-256 检查文件。应用使用 ad-hoc 签名供本机验证。

正式构建先验证上游 Mihomo，再签署并将签名后的摘要编入应用。随后签署应用和 DMG，完成 Apple 公证并将公证票据附到 DMG。[Apple 分发签名说明](https://developer.apple.com/documentation/xcode/creating-distribution-signed-code-for-the-mac/) · [Apple 公证说明](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)

### Windows

原生窗口使用 Walk 和 Win32 系统控件。

```sh
go test -race ./...
go vet ./...
mkdir -p dist/app
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-H=windowsgui -s -w" -o dist/app/KeyDockConnect.exe ./cmd/keydock-connect
python3 scripts/package-core.py dist/app
```

在已安装 Inno Setup 的 Windows 电脑上，从仓库根目录运行 `./packaging/build-installer.ps1`。Windows CI 配置了系统代理测试、TUN 路由恢复检查和安装包验收。正式安装包和 SHA-256 校验文件随 GitHub Release 发布。代理内核、离线分流数据和第三方许可包含在安装包中。

## 双平台发布

Windows 和 macOS 共用根目录的 `VERSION`。从默认分支上的发布提交推送与版本一致的 `vX.Y.Z` 标签。GitHub Actions 会在两端测试通过、macOS 安装包完成签名和公证后创建同一个 Release，包含 Windows x64 安装程序、Windows x64 免安装 ZIP、Apple Silicon DMG 和覆盖三个文件的 `SHA256SUMS.txt`。

首次发布前，将 GitHub Actions 的 `macos-release` 环境限定为版本标签，并要求发布负责人审批，或用仓库标签规则限制 `v*` 标签的创建、更新和删除。然后配置以下环境 Secrets。证书需为 **Developer ID Application**，包含私钥。公证使用 App Store Connect 团队 API Key。

| Secret | 内容 |
| --- | --- |
| `MACOS_DEVELOPER_ID_P12_BASE64` | Developer ID Application `.p12` 的 Base64 |
| `MACOS_DEVELOPER_ID_P12_PASSWORD` | `.p12` 导出密码 |
| `MACOS_NOTARY_KEY_BASE64` | App Store Connect 团队 API 私钥 `.p8` 的 Base64 |
| `MACOS_NOTARY_KEY_ID` | API Key ID |
| `MACOS_NOTARY_ISSUER_ID` | API Issuer ID |

发布任务通过 GitHub CLI 先上传到草稿，全部附件上传成功后公开。上传中断时，先检查该标签是否已有草稿，再按需补齐对应构建的附件和校验文件。[GitHub CLI 发布说明](https://cli.github.com/manual/gh_release_create)
