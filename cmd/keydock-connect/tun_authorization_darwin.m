//go:build darwin && arm64 && cgo

#import <ServiceManagement/ServiceManagement.h>

static const char *requestProxyApproval(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [SMAppService openSystemSettingsLoginItems];
    });
    return "请在系统设置的后台项目中允许 KeyDock Connect，再返回此窗口点击启用。";
}

const char *kdAuthorizeDarwinProxyService(void) {
    @autoreleasepool {
        NSString *name = @"ai.yungor.keydock-connect.tun.plist";
        NSURL *plist = [NSBundle.mainBundle.bundleURL URLByAppendingPathComponent:@"Contents/Library/LaunchDaemons"];
        if (![NSFileManager.defaultManager fileExistsAtPath:[plist URLByAppendingPathComponent:name].path]) {
            return "缺少网络代理后台组件，请重新安装 KeyDock Connect。";
        }

        SMAppService *service = [SMAppService daemonServiceWithPlistName:name];
        SMAppServiceStatus status = service.status;
        if (status == SMAppServiceStatusEnabled) return NULL;
        if (status == SMAppServiceStatusRequiresApproval) return requestProxyApproval();

        BOOL registered = [service registerAndReturnError:NULL];
        status = service.status;
        if (status == SMAppServiceStatusEnabled) return NULL;
        if (status == SMAppServiceStatusRequiresApproval || registered) return requestProxyApproval();
        return "无法注册网络代理后台组件，请从「应用程序」打开已签名和公证的 KeyDock Connect 后重试。";
    }
}
